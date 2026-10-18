# Dashboard gateways (`aether gui` and `aether-server --web-port`)

The embedded dashboard is served through two transports. `aether gui` serves
it on the user's machine and proxies the shared API and WebSocket surfaces to
the linked server. `aether-server --web-port` serves the same bundle directly
over HTTPS on the server's tailnet addresses. Both use `internal/webgate`, so
routes, framing, timeouts, close reasons, capability checks and the control
method shapes are the same; only authentication, backend and machine-local
surfaces differ. The SPA is documented in
[dashboard-frontend.md](dashboard-frontend.md), and the security stances are
in [security.md](security.md#the-dashboard-gateways).

`aether gui` uses a token on its loopback listener and an SSH-backed backend.
The server gateway has no browser token: Tailscale WhoIs identifies the source
address on every request, and the request is dispatched in-process for that
member.

## Which gateway serves what

A server with `web-port` set serves the same dashboard over HTTPS on its
tailnet addresses, identifying each request by Tailscale WhoIs
(`internal/servergw`; how to turn it on and what it needs are in
[networking.md](networking.md#the-dashboard)). Everything below describes both
gateways except where this table says otherwise: the routes, framing, timeouts
and close reasons are one implementation (`internal/webgate`), and each
gateway supplies only the identity and backend behind it.

| Surface | `aether gui` | `aether-server --web-port` |
| --- | --- | --- |
| Listener | `127.0.0.1` on an ephemeral or `--port` port, plain HTTP | the host's tailnet addresses, HTTPS with the tailnet certificate |
| Identity | per-process bearer token; the SSH backend acts for that linked member | Tailscale WhoIs on the request's source address, per request; no browser token |
| `/api/v1/*`, `/ws/events`, `/ws/attach`, `/ws/terminal` | yes | yes |
| `/local/v1/*` | yes | no - those verbs need the caller's machine |
| Backend | shared webgate over one SSH connection to the linked server | shared webgate in-process, using the same handlers as the SSH transport |
| Counts as connected for presence | while that SSH connection is open | while the browser holds a stream; `/ws/events` is one for as long as the dashboard is open |

## Running it

```sh
aether gui               # bind an ephemeral loopback port, print the URL, open a browser
aether gui --port 8080   # bind a fixed loopback port (the SPA dev proxy expects 8080)
aether gui --url         # print the URL instead of opening a browser
aether gui --json        # print one JSON line, then keep serving
aether gui --server prod # serve a named link profile (aether link --name)
```

The gateway binds `127.0.0.1` only - there is no exposure flag - and mints
a per-process bearer token that every request must carry (as
`Authorization: Bearer`, or `?token=` on WebSocket handshakes and the
initial browser tab). The printed URL is
`http://127.0.0.1:<port>/?token=<token>`. The process serves until
`SIGINT`, `SIGTERM`, or `SIGHUP`; the token dies with it.

The dashboard moves the token out of the address bar into the tab's
session storage on first load, so it is held per browser tab: a second tab
opened from a bookmark, or the same tab after `aether gui` restarted with a
fresh token, has no usable credential. Both cases answer `401` with

```json
{"error":{"code":-32001,
  "message":"a valid gateway token is required; restart `aether gui` for a fresh URL"}}
```

which the dashboard reports as an expired link, showing that message, rather
than retrying a credential the gateway has already rejected. Open the URL
`aether gui` printed again to get a working one.

`run` is the other query parameter the dashboard reads on first load.
`?run=<run_id>` opens that run's terminal, even when the local gateway has no
project clone, and stays in the address bar: the dashboard keeps its current
view in the query string, so a reload reopens the run. An authorized run link
takes precedence over optional local onboarding. A run the member cannot see
is replaced, without a history entry (`replaceState`), by the board, or by
onboarding for a member who has not finished it. This is how both shells
deliver an `aether://run/<id>` deep link - the desktop shell
(`desktop/main.js`) and the Android app (`android/`) append it to the
dashboard URL and load that.

The fragment keys `join` and `claim` carry the other two deep links,
`aether://join?edge=<relay origin>&server=<server id>&invitation=<invitation id>#<secret>`
and `aether://claim?edge=<relay origin>&server=<server id>#<secret>`
([edge.md](edge.md#join-page)). The desktop shell and the Android app in Edge
mode, whose dashboard is a local gateway, handle them; the Android app in
Tailscale mode loads a server-hosted dashboard and does not. The shell checks every part of the link and refuses any
other shape, then appends the whole link URL-encoded as
`#join=<link>` or `#claim=<link>`; a browser never sends a fragment to the
dashboard's server. The dashboard takes the fragment off the address
(`replaceState`) on first load, or on `hashchange` when the page is
already open, so the secret stays in neither the address bar nor history,
and opens onboarding's Connect step with the link.

Onboarding is an initial landing page, not a reconnect redirect. Choosing
**Manage workspaces** during startup keeps that route, and linking a clone
must not erase an in-progress wizard's workspace or first-run draft. Public
and private remote import need no local clone and remain available from
onboarding and workspace management on hosted gateways.

### Forwarding a port

A forward opens a port on `127.0.0.1` of the machine it is started on and
carries each connection to the same port in a run's container, or in your
environment's. It needs the caller's machine, so the CLI and the local gateway
can start one and the server gateway cannot:

```sh
aether forward <run-id> <port>               # a run's port on this machine
aether forward terminal <port>               # a port in your environment
aether forward <run-id> 5173 --local 8080    # on another local port
```

`run:<run-id>` names the same run as `<run-id>`. The command forwards until
it is interrupted.

In the dashboard, a run's **Browser** view lists the ports the run is
listening on. On the local gateway each one has **Open on this computer**,
which starts the forward and opens `http://localhost:<port>` in your own
browser. **More > Forward a port…** forwards a port you type and lists the
ports forwarded from that run, each a link beside **Stop**. A forward started
in the dashboard lasts until it is stopped there or `aether gui` exits. On the
server gateway the Browser view shows the `aether forward` command for the
port instead.

The server connects to the run from outside its container, so a forward
reaches a server listening on every interface (`0.0.0.0` or `::`) and not one
bound to `127.0.0.1` or `::1`. The dashboard names that address instead of
starting a forward. The run's own Browser reaches both, because it runs
inside the run's network. A CLI forward to a port that is closed or bound to
loopback accepts each local connection and closes it.

### Agent OAuth logins

When an agent prints an OAuth URL in the dashboard, click it. If the URL
contains an HTTP loopback callback, the dashboard opens a blank browser tab,
starts the local forward, and only then loads the authorization page. The
forward targets the run or the environment where the link appeared.

The server gateway has no `forward.start` verb to do that with, and the
callback port only exists on the machine that runs the forward, so opening
the link there would strand the login. Instead the dashboard leaves the link
unopened and shows a toast carrying it, the command to run on the machine
where the login will be finished, and a **Copy link** action:

```sh
aether forward run:<run-id> <port>     # a link that appeared in a run's terminal
aether forward terminal <port>         # a link in your environment
```

Links whose callback is not a loopback address open normally on both
gateways.

For a CLI terminal, or as a dashboard fallback, forward the callback port
before completing authorization:

```sh
aether forward <run-id|terminal> 1455
```

The local port defaults to the container port; use `--local <port>` only when
the OAuth redirect URI is configured for a different local port. Starting the
same dashboard forward again is a no-op, so repeated clicks keep the listener
ready.

`--json` prints exactly one line and then serves:

```json
{"url":"http://127.0.0.1:43871/?token=...","addr":"127.0.0.1:43871"}
```

That line is the contract with the desktop shell sidecar, which spawns
`aether gui --json`, parses the line, and renders the SPA itself.

Exit statuses are the other half of that contract. `aether gui --json`
exits **75** to tell the shell that `update.apply` rebuilt the desktop app
on disk: the shell calls `app.relaunch()` rather than respawning the
sidecar, so the new window and the new gateway come up together. Every
other exit keeps the shell's respawn-with-backoff behavior, which is what a
failed rebuild wants - the CLI half of the update did land, and the shell
should come back on the new binary.

On Windows the shell cannot be relaunched that way: it runs from the app
directory the update has to rename, and Windows refuses to rename a running
app's directory. There the gateway exits **76**, meaning "quit; an updater
restarts you". The shell calls `app.exit(0)` without relaunching or
respawning (`desktop/sidecar-exit.js` maps every exit status to its action).

## Design

Both transports use `internal/webgate` for HTTP and WebSocket dispatch. The
local gateway supplies an SSH-backed backend: reads and writes go through one
lazy, shared control-channel connection to the linked server, using the same
handlers as the server-hosted gateway. The server gateway supplies an
in-process backend for the member identified by Tailscale WhoIs. This keeps
the API and stream behavior independent of whether the browser is local or on
the tailnet.

For the local backend, a replay-safe call that fails on transport (a server
restart or dropped network) redials once and retries once before surfacing
`-32004` (unavailable). A method unsafe to run twice is not replayed: one
whose second run creates a duplicate (`run.launch`, `template.launch`,
`workspace.add`, `member.invite`), repeats an external side effect
(`server.update`, `terminal.exec`, `github.connect`, `run.git.*`,
`run.pr.*`, `dev.*`), or fails where the first succeeded (`run.delete`,
`run.pause`, `agent.remove`, `home.rename`, `approval.decide`). The full list is `noReplay`
in `internal/localgw/backend.go`. Their lost response may follow a committed write,
so uncertainty surfaces immediately. A subsequent explicit request can
reconnect. A call with no answer after 60 seconds fails with `-32004`
`server did not answer <method> within 1m0s` and is not retried;
`home.upload`, `home.download` and `terminal.file`, which carry up to
64 MiB, get 15 minutes. Only that call's channel closes: a slow server
handler is not a dead connection, so the connection, its streams and its
other calls stay up. The keepalive below drops a dead one. A server refusal passes through untouched as that `protocol.Error`;
in particular, busy retention admission requires an explicit retry, not
automatic replay. Streams have a separate guard: a channel that fails to open
triggers a redial only when a keepalive shows the connection is actually gone,
because tearing down a healthy connection would kill its other live streams.

A network that goes away without a reset, as when a phone sleeps or moves
between Wi-Fi and mobile data, leaves the connection open and silent. Any
byte from the server, a keepalive reply or a stream's data, proves the
connection alive, so a large download on a slow link is never mistaken for
a dead one. After 30 seconds without one the gateway sends an SSH keepalive,
and after 90 seconds without one it drops the connection, so an idle phone's
radio can sleep and a dead connection goes within two minutes. Every call
and stream waiting on a dropped connection then fails or redials. A call
already sent on it is replayed under the rule above; one that is not fails
with `-32004` and `the server stopped answering keepalives`. Requests that
arrive while a dial is in progress wait for that one dial, each only as long
as its own request lasts, and a dial gives up after 90 seconds. A dial
abandoned because the link changed, by `link.apply` or a config reload,
hands its waiting requests to a dial of the new link. The gateway reloads
`config.json` on every request whose file has changed, so `aether link` in
a terminal moves the next request onto the server it names, stopping the
forwards and syncs `link.switch` stops. A request that arrives while a
switch, a sync start or a forward start is in progress serves the current
link and leaves the reload to a later request rather than wait. A link (`link.apply`, `edge.link`, `edge.claim`,
`edge.join`) is bounded the same way, including the `server.info` call that
completes it.

A `-32004` for a failed connection carries a message prefix that says who
has to fix it, and both map to HTTP 503 as before. `network unreachable: ` means this machine could
not even attempt the connection (DNS resolution failed, or the kernel
reported no route or an interface down), so the user fixes their own
connectivity. `server unreachable: ` is everything else and is the default: a
refused connection, a dial timeout, or a failed SSH handshake, where the
server is the thing to check. The split stops at unambiguous cases
on purpose, since a refusal or a timeout cannot tell a stopped server from a
firewall, and a wrong guess sends the user to fix the wrong thing. Dial
failures are classified inside the shared dial path, so the `/ws/events`
refusal frame carries the same code and prefix as a `POST /api/v1` error.

A server whose release is newer than the CLI behind `aether gui` refuses
that CLI with `-32005` (upgrade required). The gateway passes the refusal
through as HTTP 426, with `data` set to
`{"client_version":"v0.7.0","server_version":"v0.8.0","min_client_version":"v0.8.0","command":"aether update"}`.
A WebSocket gets the same code and `data` in its ack frame, then close
1008. The gateway never redials or retries on `-32005`, because only an
update fixes it. `server.info` and `server.update_status` still answer,
and so do the `update.*` verbs, which never call the server. The server
gateway is never refused, because it runs inside the server. See
[install.md](install.md#when-the-server-refuses-an-older-client).

A failure on the way through an edge also carries error data naming the
layer that failed, so the dashboard never reports one layer as another:

```json
{"refusal":{"kind":"server-not-connected","refusal":"server is not connected to the edge"},
 "edge_path":{"layer":"server-registration","server_id":"<server id>","edge":"edge.onaether.dev",
                            "account":"github account dana",
              "online":[{"id":"<other id>","name":"prod","online":true,"role":"admin",
                         "access_policy":"approved-devices","kind":"self-hosted"}]}}
```

| `layer` | What failed | Typical cause |
| --- | --- | --- |
| `client-edge` | This machine reaching the edge | DNS, TCP, TLS or a proxy failed, the edge speaks another protocol version, or it rate-limited this address |
| `authorization` | This device's standing | Not signed in to the edge, a revoked token, not a member, a pending or revoked device, an account mismatch |
| `server-registration` | The server's own connection to the edge | Kinds `server-not-connected` (no live registration), `server-not-claimed` (connected while its claim is still being recorded; retry), `unknown-server`, or a server that did not attach |
| `end-to-end` | The relayed connection to the server | The SSH handshake, a host key that does not derive the server id, a call with no answer, or a connection the server stopped answering |

`refusal` is present when the server, the edge or this client refused,
with the refusal's `kind`. `server_id` is the server the gateway dialed and
`edge` the edge host it dialed through; `account` is the account this
device is signed in to that edge as. `online`, only for
`server-registration`, lists the other servers of that account the edge
sees online, each as the edge's server list reports it: a server
reinstalled under a new id shows up there while the link still names the
old one, and the message names `aether link --from-edge <id>`.

A link with an address carries `edge_path` only when a dial fails on both
paths, with the edge path's layer. A failure on a connection already open
carries no `edge_path`, since that connection may run over either path; a
link with an address and no edge never carries one.

The server gateway performs the equivalent identity lookup on each request.
WhoIs failures are refused before dispatch; a tagged node is denied and an
unavailable identity service is reported as `-32004`.

## Routes

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/` and any other non-API path | the SPA (fallback to `index.html`) |
| `POST` | `/api/v1/<rpc.method>` | any control-channel method, dispatched through the shared webgate |
| `GET` | `/api/v1/run/<run_id>/patch` | `run.patch` |
| `GET` | `/api/runs/<run_id>/terminal-history` | non-dashboard compatibility stream of the complete retained raw ANSI archive |
| `POST` | `/api/runs/<run_id>/terminal-history` | legacy form compatibility for that raw archive; not a dashboard action |
| `GET` | `/api/v1/disk` | `server.disk` |
| `GET` | `/api/v1/capabilities` | what this gateway can do |
| `GET` | `/ws/events` | event subscription (WebSocket) |
| `GET` | `/ws/attach/<run_id>` | PTY attach (WebSocket) |
| `GET` | `/ws/attach/<run_id>?shell=<tab>` | writable run-container shell tab (WebSocket) |
| `GET` | `/ws/acp/<run_id>` | the session item stream of a run with `acp: true` (WebSocket) |
| `GET` | `/ws/assistant` | the caller's assistant session: `AssistantFrame` lines, each an item, a reset, or the whole `AssistantStatus` (WebSocket). A status frame follows the ack and every change of the state or the pending confirmations. Header `{"after_seq":0}`; the ack is an `ACPStreamResponse`. There is no control lease. Every end other than a revocation closes 1012 and the client resubscribes from its last seq |
| `GET` | `/ws/terminal?tab=<tab>` | persistent member environment terminal (WebSocket) |
| `GET` | `/ws/dev/browser/<run_id>` | observation-only binary browser frame stream |
| `GET` | `/api/v1/dev/<run_id>/artifacts/<artifact_id>` | transient capture bytes; add `?evidence_packet_id=<packet_id>` for the retained copy |
| `GET` | `/api/v1/run/<run_id>/messages/<message_id>/attachments/<index>` | persisted chat image bytes; zero-based attachment index, requires View on the run |
| `POST` | `/local/v1/<verb>` | client-machine verbs, on `aether gui` only |

Anything that is not `/api/`, `/ws/`, or `/local/` is served from the
embedded `web/dist`, without authentication - the SPA bundle is not secret
and has to load before the gateway can authenticate API calls. Unknown paths
fall back to `index.html` so client-side routing works on a hard refresh. An
`/api/`, `/ws/`, or `/local/` path hit with the wrong method is the exception:
it answers `405` with a JSON error body instead of falling through to the SPA,
so a wrong-verb client bug cannot masquerade as a `200`. Ordinary JSON request
bodies, including `/local/v1` calls, are capped at 1 MiB. `terminal.image` has
a 12 MiB HTTP body cap for base64 and JSON framing; decoded images are capped
separately at 8 MiB. `terminal.file` has 96 MiB for a decoded 64 MiB file.
File and configuration exceptions are listed below.

### Chat image previews

The chat image route identifies a persisted room message and its attachment
index, never a client-selected filesystem path. The message must belong to the
requested run and workspace. Both gateways stream the validated PNG, JPEG,
GIF or WebP bytes through the existing binary artifact transport, with
`no-store`, `nosniff` and sandbox headers. It works after the container stops;
deleting the run/message or revoking View access prevents further downloads.

The artifact stream request carries `room_message_id` and `attachment_index`
with canonical `artifact_id: "room:<message_id>:<index>"`. These selectors
cannot be combined with `evidence_packet_id`; room images use View authority,
not the Steer authority of live development captures.

### Development streams and retained captures

Development control calls use the existing `POST /api/v1/dev.*` RPC routes.
Transient capture downloads, browser observation and development-terminal
attaches require **Steer** and access to the run's backing account. Adding
`evidence_packet_id` to the existing artifact route selects the immutable
retained copy instead: the server checks packet/run/artifact identity and
existing evidence **View** permission and expiry, including during transfer.
This broader access is why retention must be deliberate. Downloads stream
bytes with the recorded content type and length, `Cache-Control: no-store`
and attachment headers; they do not return base64 in a control response.

`/ws/dev/browser/<run_id>` accepts a JSON page target (`session_id`, `page_id`,
`page_revision`) and returns a JSON acknowledgement or refusal. Each following
binary message is one complete frame: two big-endian 32-bit lengths, JSON
metadata (at most 16 KiB), then image bytes (at most 2 MiB). Metadata carries
run/session/page/revision/viewport identity and image dimensions; clients must
honor these fences. Input goes through `dev.browser.action`, never this stream.
Late subscribers receive the latest complete frame of the active stream;
slow viewers skip obsolete frames rather than building an image backlog.

The existing `/ws/attach/<run_id>?shell=<terminal_id>` attaches to the shared
development terminal, including one started by an agent. Retain the
acknowledgement's `terminal_id` and `incarnation`; reconnect with the incarnation
to avoid attaching to an explicit replacement. `server_owned_responder: true`
means the server answers terminal queries; viewers must not send competing
device replies. Writer ownership is fenced by surface, incarnation,
`control_session_id` and `control_generation`. Viewer attach does not resize
the app; resize and stop require explicit control. `dev.terminal.list` names
who started each terminal in `started_by` (`run_agent` or `member`).
Development control changes
use `dev.control.*` and reconnect, not interactive primary-agent attach.

`dev.port.list` takes `{"run_id":"..."}` and returns the TCP ports listening
in the run's network namespace, lowest first:

```json
{"ports":[{"port":5173,"address":"::1","process":"node"},
          {"port":8000,"address":"0.0.0.0","process":"python3.12"}]}
```

`address` is what the port is bound to. `process` is the owner's executable
name, absent when the run's user cannot see the owner. A port bound more than
once is listed once, by its widest binding: every interface before one
address before loopback. The server reads the list with one `/bin/sh` exec in
the run container, which needs `grep` and `ls` there, so the run must be live
and not paused. Run agents cannot call it; they are inside that namespace.

Cancellation and authority revocation stop source work and close download
readers before waiting for SSH status or close messages. A stalled SSH close
has a bounded grace period before the underlying transport is aborted; this
can end sibling streams on that connection. Responsive peers retain their
other channels. HTTP downloads also have bounded writes and abort on
interrupted/short sources rather than returning a successful truncated capture.
These transport bounds do not automatically replay a development mutation.

### Terminal history

The dashboard browses recorded output through
`POST /api/v1/terminal.history`. Like every gateway RPC, it uses the local
bearer token or server-side Tailscale WhoIs identity; the guarded method also
requires **View** permission for the named run. A member who cannot view the
run cannot page or search its transcript.

The request names `run_id`, an optional opaque `before` cursor returned as
`next_cursor` by the previous response, an optional case-insensitive literal
`query`, and a bounded `limit`. An omitted or zero limit defaults to 100; the
server caps it at 200. Queries are limited to 256 UTF-8 bytes. A cursor must be
returned unchanged and used with the same run and query; it is authenticated
server state, not an offset for clients to construct or edit. Malformed,
tampered, or mismatched cursors are rejected as invalid parameters.
An authenticated cursor whose segment has since been pruned instead returns
unavailable (`-32004`, `earlier terminal history has expired`). Clients must
not substitute an empty page or silently restart at the newest output.

The first request starts at the newest retained output. Every response orders
its normalized text lines chronologically and returns `has_more`; when another
window is available it also returns `next_cursor`, which requests the next
older window. Search applies the literal query while scanning retained casts
and returns only matching normalized lines. Escape sequences and terminal
controls are interpreted into text rather than sent to the browser.
`truncated_before:true` means an earlier prefix has expired. It is derived
from the same retained-history snapshot as the page, including empty pages;
`has_more:false` means the retained boundary, not necessarily the run's start.

Line count is not the only bound. Each request also limits raw disk reads,
decoded bytes and events, decoding time, cast segments per page and concurrent
readers; searches have their own lower concurrency cap. Directory discovery
streams with bounded memory and caller cancellation, not a total-history
entry cutoff. A page or search may therefore return fewer than the requested
number of lines, including none, with `has_more:true`. Continuing uses an older-page request
with `next_cursor`; the live attach does not do this work. The dashboard's
integrated upward scroller starts at the newest page and prefetches older
windows near the loaded edge, automatically continuing empty scan windows.
It never writes archive pages into xterm. Its existing Find searches retained
loaded pages and frozen screen rows locally, rather than issuing server-search
queries. The protocol's `query` option and bounds remain available unchanged.

The browser renders a bounded visible-row window, retaining pages and saved
view state in IndexedDB with an eight-page resident text LRU rather than
discarding older lines. Prepending pages preserves the cursor-linked row,
relative pixel offset and horizontal offset. A frozen VT presentation is
separated from normalized recorded text by an inline boundary; the two are
not text-deduplicated, and normalized history is not exact historical VT
reconstruction. Leaving a run closes its main attach but keeps its static
read position. A fresh live bootstrap on return does not overwrite that view.
Scrolling down to the bottom or `End` returns live; only a subsequent new
reading episode refreshes paging from the newest archive head.

Cache state is fenced by identity, terminal-data epoch and run creation
identity, and invalidated for deleted runs. Storage failures are reported;
an in-memory fallback when storage is unavailable is not a persistence
guarantee. These are browser policies, not changes to the request, cursor or
authorization contract.

The raw `GET /api/runs/<run_id>/terminal-history` route remains only for
non-dashboard compatibility consumers that need all currently retained raw
bytes, not a guarantee of the complete recording from run start. The
dashboard neither calls it nor exposes a full-history download action. The
route performs the normal gateway and member authorization, then streams the
available cast incarnations as raw ANSI bytes with
`Content-Type: application/octet-stream`, `Cache-Control: no-store`, and
`Content-Disposition: attachment; filename="terminal-history-<run_id>.ansi"`.
It supplies the archive byte count as `Content-Length`; the stream ends at the
finite archive boundary captured for the request even if the run is live. It
does not parse the bytes into a screen snapshot, buffer the archive in memory,
acquire control, or write to the PTY. The same-origin rule and the gateway's
normal bearer-token or Tailscale WhoIs authorization apply. If the run has no
retained transcript, the route returns the gateway's normal not-found or
unavailable refusal rather than an empty archive.

The `POST /api/runs/<run_id>/terminal-history` form route remains for legacy
compatibility clients. Its body is `application/x-www-form-urlencoded`, no
larger than 8 KiB, and may contain only an optional `token` field. When
present, the gateway copies that token into a cloned `Authorization: Bearer`
check; it never accepts the token in the URL. The original `Origin` and normal
member authorization still apply. The response streams directly with the same
raw-export headers and finite archive boundary as `GET`; errors remain JSON.
It does not build a Blob or buffer the archive in the browser.

### `POST /api/v1/<method>`

The path segment after `/api/v1/` is the JSON-RPC method name, dots
included: `POST /api/v1/run.list`. The request body is the method's
`params` object (an empty body means no params) and must be declared
`Content-Type: application/json`; any other content type answers `415`
before the body is read. A request carrying an `Origin` header that is not
the gateway's own host answers `403` before anything else, on every route
(the cross-site rule in [security.md](security.md#the-dashboard-gateways)).
Success is `200` with the method's result object as the whole body;
failure is a non-2xx status with the JSON-RPC error object wrapped:

```json
{"error":{"code":-32001,"message":"run.kill: permission denied"}}
```

Status mapping (the code is the authority; the status is a convenience):

| JSON-RPC code | HTTP |
| --- | --- |
| `-32700` parse, `-32600` invalid request, `-32602` invalid params | 400 |
| local bearer token missing or expired | 401 |
| `-32001` denied, including a tagged WhoIs node, and a foreign `Origin` header | 403 |
| `-32004` unavailable, including an unavailable WhoIs identity lookup | 503 |
| `-32005` upgrade required: the server's release is newer than this CLI | 426 |
| `-32600` invalid request: body not `application/json` | 415 |
| `-32000` not found | 404 |
| `-32002` invalid state, `-32003` conflict | 409 |
| `-32603` internal | 500 |

Param and result shapes are the ones in `internal/protocol` (`wire.go` and
the per-feature files), unchanged by this transport, and every call passes
the same capability and member-authorization checks regardless of transport.

`run.list` and `run.get` include optional `mission_id`, `mission_role`, and
`integrator_run_id` fields on each run snapshot. The current swarm integrator
has role `integrator` and points to its own run ID; workers have role `worker`
and point to that swarm's current integrator, including finished and older
attempts. Replacing the integrator changes that parent ID on worker snapshots
and removes the swarm fields from the replaced integrator. Ordinary runs
omit all three fields. A worker linked to conflicting swarms also omits
them rather than choosing an arbitrary parent; repeated attempts within one
swarm retain that swarm. These fields come from durable relationships,
not task text, and confer no authorization.

`account_member_id` on a run snapshot is the account the run uses.
`requested_account_member_id` appears only when [usage
fallback](teams.md#usage-fallback) launched the run on another account than
the one asked for, and holds that one; after a mid-run move it holds the
account the run first used, and is absent again if the run moves back to
it. Each move publishes a `run.account` event `{account_member_id,
previous_account_member_id, requested_account_member_id}` carrying the same
fields. A headless Claude run whose output reports the usage limit
publishes `run.usage_limit` `{message}` with the agent's message. `run.launch` and `mission.create`
take the fallback policy as `fallback_account_member_ids` (member IDs, tried
in order) or `no_fallback: true`; omitting both orders the launcher's
accounts by usage left, and sending both is refused with `-32602`
`fallback_account_member_ids and no_fallback cannot both be set`. When no
account qualifies, the run launches on the requested account. Swarm
attempts carry `launch_account_id`, the account the worker's run launched
on.

Run snapshots carry `status_changed_at` (RFC 3339): when the run entered its
current `status`, or its current `reason` while it is `needs-attention`. A
same-status rewrite of the reason, such as retention expiry relabelling a
finished run, leaves it alone. It is absent on a run whose status last
changed before the server recorded it; a client then estimates from
`finished_at`, `started_at` or `created_at`.

A live run (`running` or `needs-attention`) carries `state`: `working`,
`waiting-agents`, `waiting-ci`, `waiting-tools`, `waiting`, `needs-you`,
`finished`, `failed` or `disconnected`
([run activity](failure-handling.md#run-activity)); it is absent on any
other status. Rows and events written before `waiting` existed carry `idle`,
which means `waiting`. `status_seq` is always present, starts at 1 and
increases on every published change of status, reason, state,
`outcome_unseen`, `finish_unopened` or `review`. Every run carries `review`
(`""`, `ready`, `later` or `accepted`) and, once it has changed,
`review_changed_at` (RFC 3339) ([review](failure-handling.md#review)).
Every `run.status` payload carries the run's `state`, `status_seq`,
`review` and `review_changed_at` after it; ignore one whose `status_seq` is not
greater than the one you hold. A `run.status` event stored before the server
had `status_seq` carries `0` and no `state`: label it by its status alone.

`run.launch`, `template.save` and `worker.start` take an optional `model`,
and `mission.create` takes one on the integrator and on each of
`execution_choices`; it is passed to the agent as is, and empty keeps the
agent's default ([harnesses.md](harnesses.md#launch-argv)). A model with
whitespace or control characters, a leading `-`, or more than 128 bytes is
`-32602` with
`model "x y" must be a single argument: no whitespace or control characters, not starting with "-", at most 128 bytes`.
Run snapshots and templates carry the `model` they launch with, omitted when
empty.

Run snapshots, including mutation results, also expose
`container_retained_until` (RFC 3339), `cleanup_pending`, and `cleanup_error`
when applicable. These describe the retained runtime, not the lifetime of
files, history, or published Git results. Changes arrive as `run.retention`
events with a complete replacement of `container_retained_until`,
`cleanup_pending`, and `cleanup_error`; omitted fields clear prior values.
Unchanged cleanup retries publish nothing. These events do not change status,
completion timestamps, or the outcome-unseen flag and must not be treated as
another `run.status` completion.

#### Independent execution and input state

`run.get` and `run.list` always include `pending_inputs`, an array of
`{"id":"request-1","session_id":"session-a","kind":"question"}` objects.
An empty set is `[]`, never `null`. Execution remains in `status`: a
`running` run can have pending input while another session continues
working, and a `needs-attention` run need not have any. The dashboard's
**Needs you** state is derived from both and is not a new execution status
or answer transport.

Native reporters send `run.report` through their run-scoped coordination
socket, not the member's gateway control endpoint. The socket supplies the run
identity. The report accepts the existing `state` (`working` or `waiting`),
optional `reason`, and optional `input_updates`:

```json
{
  "state": "working",
  "input_updates": [
    {"operation":"open","session_id":"session-a","kind":"question","id":"request-1"}
  ]
}
```

`waiting` reports that the turn ended, not an unresolved question. `state` can be
omitted only when `input_updates` is nonempty; an input-only report preserves
execution, including when the last request closes. `reason` is execution
metadata, not a prompt body or evidence of input. An optional `session_id`
names the agent's own top-level session; a Standard run stores the latest one
as `harness_session_id` so [a mode switch](enhanced-runs.md#switching-a-running-agent)
can resume it. One that is not 1-128 characters of letters, digits, `.`,
`_`, `:` and `-` is ignored and the rest of the report applies.

| Operation | Fields | Meaning |
| --- | --- | --- |
| `open` | `session_id`, `kind`, `id` | Add this exact unresolved request. |
| `close` | `session_id`, `kind`, `id` | Remove only this identity; the same ID in another session or kind is unaffected. |
| `clear` | `session_id` | Remove one session's requests when the adapter has evidence that the session terminated. |
| `replace` | `requests` | Replace the adapter's complete pending set; `[]` clears it. Each entry has `session_id`, `kind`, `id`. |

Kinds are `question`, `permission`, `form`, and `extension_ui`. IDs and session
IDs must be valid UTF-8, nonblank, control-free strings of 1–256 bytes. There
are at most 128 updates per report, 128 requests per replacement, and 128
pending requests per run. Unknown operations/kinds, missing identities, and
conflicting operation fields are rejected rather than normalized or truncated.
`open`/`close` do not accept nonempty `requests`; `clear` accepts no request
identity or request list; `replace` accepts no top-level request identity.
Reports carry only correlation metadata, never prompts, answers, paths, or
transcripts.

Repeated opens, closes, and unchanged replacements are idempotent. Turn
completion, silence, prose, and idle execution do not clear outstanding input.
The pending set is persisted independently from the last execution report and
survives server restart for still-live runs. Actual terminated or relaunched
run lifetimes discard it; stale saved snapshots cannot restore a previous
lifetime's requests. Persistence errors are returned to the reporter rather than
announcing input state that was not saved.

Each actual change publishes a durable `run.input` event with payload
`{"pending_inputs":[...]}`; closing the last request publishes
`{"pending_inputs":[]}`. This is a complete replacement snapshot, independent
of `run.status`, and uses the existing event envelope and replay sequence on
`/ws/events`. An unchanged set emits no event unless a previous publication
failed: retrying the report or recovering the live run publishes the current
set, including an empty set after the last close. Clients must preserve
events received after a snapshot request began when merging that response,
so an older `run.get`/`run.list` response cannot resurrect closed input.


`run.release` accepts `{"run_id":"..."}` and returns `{}`. It uses the same
`Kill` permission as `run.kill`, but only releases a finished run's retained
container and browser companion. It preserves the outcome, archive state,
checkout, transcript and run record under their existing retention rules.
A run whose container was released cannot be reopened. A run that has become active is
rejected with `-32002`; repeating a successful release is a no-op. Evidence
capture or runtime cleanup errors are returned without claiming release.
The dashboard offers this as **Free container…** on one run and, for the
selected workspace including archived runs, **Free retained containers…**.

`run.delete` uses the same `Kill` capability as `run.kill` and accepts the
same `{"run_id":"..."}` params. For a live run it stops the container and
waits for supervision to publish the final branch before removing the
checkout, transcripts and run-owned database records. For an old run it
removes the checkout and transcripts directly. The run's timeline remains as
audit history.

`workspace.delete` is admin-only and accepts `{"workspace_id":"..."}`,
returning `{"ok":true}`. It permanently removes an inactive workspace and its
server-side data, repository and mirror keys. It preserves member accounts,
homes, local clones and upstream repositories. Revoke remote deploy keys
separately. Active runs, pending runtime or swarm work, configured schedules,
active candidate verification and unfinished delivery block deletion; the
error names the blocker. In-flight control or Git operations return `-32003`
instead of waiting behind them. Cleanup errors leave the workspace available
for retry but do not restore already removed data. Success publishes
`workspace.deleted` with the workspace ID in the event envelope and `{}` as
the payload.

`run.archive` also uses the `Kill` capability and accepts
`{"run_id":"...","archived":true}`. It hides a finished run from the board
and keeps its data, restorable any time: only a run in a final disposition
(`merged`, `abandoned`, `failed`, or `interrupted`) can be archived, and
archiving any other status returns `-32002` naming the run's real status.
Archiving an already-archived run is a no-op that leaves its timestamp
unchanged; calling it with `"archived":false` restores the run. The response
is a `RunResult` with `run.archived_at` set while archived and absent otherwise.
Archiving does not schedule deletion; the obsolete `deletes_at` field is no
longer emitted. Both calls publish `run.archived` with `archived_at` (null on
restore) and a matching timeline note. Explicit Delete remains separate.

`mission.archive`, `mission.unarchive` and `mission.delete` accept
`{"mission_id":"..."}`. Archive and unarchive return `{"mission":Mission}`
with `archived_at` set while archived; delete returns `{}`. They need
`run.launch` and the swarm's accountable human or an admin, archive and
delete cascade to the swarm's runs, and a swarm with a live run is refused
with `-32002`; see
[coordination.md](coordination.md#archiving-and-deleting-a-swarm).

`member.rename` accepts `{"display_name":"..."}` (and `member_id`, admin
only, to rename someone else) and returns `{"member":Member}`. The name is
trimmed and must be 1 to 64 characters with no control characters; a bad
name is `-32602` with the reason. Each rename publishes `member.changed`
(`{"member_id":"...","display_name":"..."}`) in every workspace.

`run.seen` accepts `{"run_id":"..."}` and returns a `RunResult`. It records
that the caller opened the run, which clears up to two flags on it:

- `run.outcome_unseen` is set by an agent's success or failure outcome (see
  [coordination.md](coordination.md)), including when an interactive run
  stays open at `needs-attention`. Only a call from the run's current owner
  clears it; a call from anyone else, admins included, leaves it set.
- `run.finish_unopened` is set by that same outcome, and by any change into a
  terminal status (`completed`, `failed`, `interrupted`, `merged` or
  `abandoned`) that no member asked for: an exit, a failure, a server
  restart, or a swarm completing or stopping its workers. A member's own
  `run.close` or `run.kill`, and the closes `mission.archive` makes for them,
  leave it clear whatever it was before: that member has dealt with the run.
  That includes a close or kill of a run that had already finished. When the
  call relabels the run, its `run.status` event carries the cleared flag;
  when it changes nothing else, the server publishes `run.finish_opened`.
  `run.release` frees a container without dismissing the run and leaves the
  flag as it was. A `run.seen` call from any member who may view the run
  clears it. Runs that finished before the server had this flag never carry
  it.

One call publishes at most one event, with an empty payload:
`run.outcome_seen` and a timeline note when it cleared `outcome_unseen`, which
also tells clients that `finish_unopened` is clear; otherwise
`run.finish_opened` when it cleared `finish_unopened`. A call that clears
neither returns the run and publishes nothing. Every `run.status` payload
carries both flags as the run's flags after that event. A same-status
re-label such as retention expiry keeps them. `outcome_unseen` is `true` when
a reported outcome becomes reviewable until the owner opens it, `false` after
work resumes, Close or another status change. `finish_unopened` is `true`
from the finish until a member opens or closes the run, `false` after work
resumes.

`run.review` accepts `{"run_id":"...","action":"complete"}` or
`"action":"later"` and returns a `RunResult`. It needs the kill permission,
as `run.close` does. `complete` closes a live run as `run.close` does but
records `completed`, or only records the acceptance on a `completed` run,
and sets `review` to `accepted`; it is idempotent once accepted, and refused
with `-32002` for other terminal statuses and for a live swarm worker, which
completes through its integrator. `later` without a `ready` or `later`
review is `-32002` too. `later` sets
`review` to `later` and leaves the run where it is.
Each change publishes `run.review` with
`{"review":"...","review_changed_at":"...","status_seq":N}`; apply it only
when `status_seq` is above the one you hold.

The `push.*` methods manage Web Push for the caller's own browsers
([networking.md](networking.md#notifications)). `push.status` accepts an
optional `{"endpoint":"..."}` and returns
`{"public_key":"...","subscribed":false}`: the server's VAPID key as the
unpadded base64url point a browser subscribes with, and whether the caller
has a subscription with that endpoint. `push.subscribe` takes a browser
`PushSubscription` as its `toJSON` gives it,
`{"endpoint":"https://...","keys":{"p256dh":"...","auth":"..."}}`. Anything
but an `https` endpoint with valid keys is `-32602`, and a seventeenth device
for one member is `-32002`. `push.unsubscribe` and `push.test` take
`{"endpoint":"..."}`; an endpoint that is not the caller's is `-32000`.
`push.test` sends one notification before it answers, and when the push
service does not take it the error is `-32004` carrying that service's
answer. `push.active` takes no params and records that the caller is using a
dashboard, which holds their notifications until they have left it alone for
a minute. Every method but `push.status` returns `{}`.

`run.relaunch` is another proxied control-channel method:

```sh
aether reopen <run-id>
```

The equivalent gateway call is `POST /api/v1/run.relaunch` with
`{"run_id":"run_..."}` and a `RunResult` response. It is eligible for a
Standard or Enhanced run that retained its container and whose
`--run-container-ttl` deadline has not passed: `merged` or `abandoned`
after `run.close` (`reason` is `closed; retained container`). Older
agent-finished retained runs remain eligible. Interactive outcome reports
now leave the run open and do not require reopening. `run.relaunch` resumes the
same run row in the same container and checkout; it does not create a new run
or container and does not perform a new launch or disk-floor admission.

Retention expiry is swept within at most one minute and reconciled on server
boot. Once expiry destroys the retained container, the call returns `-32002`
(invalid state, retained container unavailable), and the run cannot be
reopened. An expired or otherwise unavailable retained run cannot be
reopened; a row removed by `run.delete` instead returns not found. The
default `--run-container-ttl` is `168h` (7 days); negative values disable
retention, so a closed interactive run is unavailable to `run.relaunch` immediately.

### `GET /api/v1/capabilities`

```json
{"gateway":"local","methods":["*"],"ws":["events","attach","acp","assistant","terminal","dev/browser"],
 "local":["daemon.install","daemon.status","edge.claim","edge.hostkey","edge.join","edge.link",
          "edge.login","edge.logout","edge.servers","edge.status","env.agents","forward.start",
          "forward.status","forward.stop","git.identity","link.apply","link.repo",
          "link.status","link.switch","pull","pull.switch","repo.fast-forward",
          "repo.push","sync.start","sync.status","sync.stop",
          "update.apply","update.check","update.status","workspace.selection"],
 "version":"v1.2.3","commit":"abc1234"}
```

The server gateway answers the same shape with no `local` field because it
cannot run verbs on the browser's machine:

```json
{"gateway":"server","methods":["*"],"ws":["events","attach","acp","assistant","terminal","dev/browser"],
 "version":"v1.2.3","commit":"abc1234"}
```

`methods` is `["*"]` because both transports dispatch every control-channel
method; `ws` lists the WebSocket surfaces served; `local` is the sorted
`/local/v1` verb list, absent where there are none. A client probes this
descriptor rather than hard-coding its transport. The SPA gates local linking,
folder picking, Git operations and updates independently of shared server
onboarding. Git identity, remote repository import, agents, GitHub connection,
Files and member configuration work through either gateway when their methods
are available.

`version` and `commit` are the build serving the gateway, which is the only
way the SPA can learn what CLI it is running against - `server.info` answers
for the server. Both are absent on a gateway that predates them.

### Control-channel methods this gateway calls

The two `GET` endpoints above are backed by control-channel methods, as are
the file reads and the member and workspace writes below. `aether gui`
proxies these methods over SSH; the server gateway dispatches them in-process.
Both transports therefore expose the same API shape and authorization checks.

### Enhanced-run methods

These act on the agent session of a run with `acp: true`: an
[enhanced run](enhanced-runs.md), or a
[background run over ACP](enhanced-runs.md#background-runs). The first
three need **Steer** and the run's control lease (`control_session_id` and
`control_generation`, from `/ws/acp` or a terminal attach); without them the
call is refused with `-32602`, and with a lease another session holds with
`-32003`.

| Method | Request body | Success result |
| --- | --- | --- |
| `run.input.answer` | `{run_id, request_id, option_id, values, control_session_id, control_generation}`; `values` is the form answer object for `accept` on a form question, omitted otherwise | `{}`; a request already answered or cancelled is `-32003` with `data.reason` `already_answered` |
| `run.acp.cancel` | `{run_id, control_session_id, control_generation}` | `{}`; pending requests are answered `cancelled` |
| `run.acp.set_option` | `{run_id, option_id, value, control_session_id, control_generation}`; `value` is a value id string or a boolean | `{}`; the agent's new option list arrives as a `config_options` item. A model selection also updates the run's `model`, so a reopened session keeps it |
| `run.acp.history` | `{run_id, before_seq, limit}` (View); `before_seq` 0 reads from the newest, `limit` at most 1000 | `{frames: [...], oldest_seq, truncated_before?}`, oldest first, cut like stream frames; metadata and items are one atomic retained page |
| `run.acp.item` | `{run_id, seq}` (View) | `{item: {...}}`, whole; expired IDs return unavailable with an explicit retained-history expiry message, missing IDs return not-found |
| `run.mode.switch` | `{run_id, mode, control_session_id, control_generation}`; `mode` is `tui` (Standard) or `acp` (Enhanced); the lease may be omitted while nobody holds the run's control | `{run: {...}}` once the switch is done; see [Switching a running agent](enhanced-runs.md#switching-a-running-agent) |

Use the option IDs and values from the session's `config_options` snapshot,
including selectors synthesized from an agent's separate `models` or `modes`.
The host routes those selectors to `session/set_model` or `session/set_mode`;
native config selectors use `session/set_config_option` and take precedence.
Model and mode notifications received during an option change remain
authoritative if that request later returns an older option snapshot.
A mode-only notification preserves that mode without discarding accepted model,
effort or other option changes returned by the request.

A run whose session is not running answers `-32004` with the reason.
`run.mode.switch` also takes a Standard run, needs **Steer**, and while
anyone holds the run's control needs that lease (`-32003` otherwise). It
answers `-32002` with the real error as the message and a `data.reason` the
dashboard can act on: `not_switchable` for an agent whose `agent.list` entry
has `switchable: false`, `session_not_reported` for a Standard run whose
agent has not reported its session yet (it does on its first turn), and
`adapter_not_installed` for a switch to Enhanced while the agent's ACP
server is not installed. Any other run it cannot switch now answers
`-32002` with the reason and no `data`. A run snapshot carries `switching` (`tui` or
`acp`) while a switch is in flight; a `run.mode` event
`{mode, previous, switching, reason}` with the same `switching` opens each
switch, and one without `switching` closes it.
`run.inject` takes `steer: true` to add a message to the agent's running
turn, and `control_session_id`/`control_generation` to deliver it at once
rather than after the room's moderation delay; its result's `outcome` is
`sent`, `queued` or `injected` for an enhanced run. A `queued` message
carries `agent_delivery: "queued"` until the agent takes it
(`"delivered"`), or turns `not_sent` with `failure.code` `agent_refused` or
`agent_disconnected`; each change publishes `workspace.room_message`
([enhanced-runs.md](enhanced-runs.md#permissions-and-input)).

`run.inject` also accepts `attachments`, an ordered array of at most eight
references returned by `terminal.image` or `terminal.file`. Text may be empty
only when something is attached. References remain separate in the persisted
room message. Enhanced delivery resolves validated image bytes into ACP image
blocks; their short `aether://room/<message_id>/<index>` URIs identify
authorized previews, not public download URLs. Image bytes are not copied into
the session item log. Each other file becomes a `resource_link` block whose
`uri` is the `file://` URL of its container path, with `name`, `size` and a
`mimeType` sniffed from its first 512 bytes; the agent reads the file itself,
and the session item log keeps that URI. An image attached for an agent
without `prompt_images` refuses the message as **Not sent**; other files need
no capability.

### Candidate integration methods

The authenticated gateway exposes the candidate integration service through
the same generic control-channel route. `aether gui` sends these calls over
its authenticated SSH backend; `aether-server --web-port` dispatches them
in-process for the member identified by Tailscale WhoIs. The method names,
parameter fields, aggregate states, and result objects are documented in
[integration.md](integration.md).

| Method | Request body | Success result |
| --- | --- | --- |
| `integration.prepare` | `IntegrationPrepareParams` | `{ "candidate": Candidate }` |
| `integration.show` | `IntegrationShowParams` | `{ "candidate": Candidate }` |
| `integration.patch` | `IntegrationShowParams` | `{ "patch": string, "truncated": boolean }` |
| `integration.list` | `IntegrationListParams` | `{ "candidates": CandidateSummary[] }` |
| `integration.resolve` | `IntegrationResolveParams` | `{ "candidate": Candidate }` |
| `integration.verify` | `IntegrationVerifyParams` | `{ "candidate": Candidate }` |
| `integration.request_delivery` | `IntegrationRequestDeliveryParams` | `{ "candidate": Candidate }` |
| `integration.decide` | `IntegrationDecideParams` | `{ "candidate": Candidate }` |
| `integration.deliver` | `IntegrationDeliverParams` | `{ "candidate": Candidate }` |
| `integration.delete` | `IntegrationDeleteParams` | `{}` |

For example, the dashboard sends the params object directly (not a JSON-RPC
envelope):

```http
POST /api/v1/integration.show
Content-Type: application/json
Authorization: Bearer <local-gateway-token>

{"workspace_id":"ws_123","candidate_id":"cand_456"}
```

The response is `200` with the result object as the whole body. Every
integration mutation is authenticated and re-authorized at the service
boundary; a client cannot supply an actor, run identity, swarm authority,
or push grant. A transport failure is handled by the existing local-gateway
redial/retry policy, while a service denial, conflict, stale revision, or
unavailable owned source remains the server's protocol error.

| Method | Params | Result |
| --- | --- | --- |
| `run.patch` | `RunPatchParams` (`{"run_id":"...","from":"...","to":"..."}`; `from` and `to` optional, `to` only with `from`) | `RunPatchResult` - the same JSON shape the patch `GET` answers |
| `coord.messages.list` | `{"workspace_id":"...","mission_id":"...","run_id":"...","correlation_id":"...","before":"...","limit":50}` (only `workspace_id` required; `run_id` matches either side) | `{"messages":[{"id":"...","workspace_id":"...","mission_id":"...","from_run_id":"...","to_run_id":"...","kind":"message"\|"question"\|"reply"\|"report","correlation_id":"...","body":"...","created_at":"...","delivered_at":"...","acked_at":"...","outcome":"...","summary":"...","next_action":"..."}],"next_before":"..."}` newest first; report fields only on `report` rows. See [coordination.md](coordination.md#seeing-agent-messages) |
| `server.disk` | none | `ServerDiskResult` - the same JSON shape the disk `GET` answers |
| `server.sysbox.status` | none; admin only | `{"state":"on"\|"off"\|"unsupported"\|"installing"\|"failed","reason":"...","log":"..."}` - whether Docker lists `sysbox-runc`. `reason` explains `off`, `unsupported` and `failed`; `off` with a reason naming `sudo aether-server sysbox install` means the server is not root. `log` is the last 8 KiB of installer output |
| `server.sysbox.install` | none; admin only | the same status. Starts the installer in the background, one at a time; a call while one runs returns `installing`. Poll `server.sysbox.status` until `on`, `unsupported` or `failed`. The server then uses Sysbox for new containers at once. Never retried after a transport failure. While it runs, `server.update` answers `-32003` |
| `account.usage` | `{"account_member_id":"<member-id>","refresh":false}` (`account_member_id` may be empty for the caller's account) | `{"account_member_id":"<member-id>","providers":[{"provider":"claude"\|"codex","status":"ok"\|"stale"\|"unauthenticated"\|"unsupported"\|"unavailable"\|"error","windows":[{"id":"...","label":"...","used_percent":12.5,"resets_at":"2026-09-18T13:00:00Z"}],"plan":"...","updated_at":"2026-09-18T11:59:00Z","checked_at":"2026-09-18T12:00:00Z","retry_at":"...","error":"..."},...]}` |
| `insights.get` | `{"workspace_id":"","scope":"mine"\|"everyone","from":"<RFC3339>","to":"<RFC3339>","time_zone":"America/Los_Angeles"}` | `{"generated_at":"...","from":"...","to":"...","time_zone":"...","bucket":"day"\|"week","totals":Totals,"previous":Totals,"series":[{"start":"2026-10-09","launched":3,"completed":2,"merged":1,"failed":0,"interrupted":0,"abandoned":0}],"heatmap":[{"date":"2026-04-20","runs":2}],"by_workspace":[{"workspace_id":"...","name":"...","launched":4,"finished":3,"failed":1}],"by_agent":[{"harness":"claude","model":"","launched":4,"finished":3,"failed":1}],"swarms":{"launched":1,"completed":1,"cancelled":0,"agents":5,"tasks_accepted":4},"durations":{"runs_measured":9,"median_run_seconds":1260,"p90_run_seconds":4100,"state_runs_measured":6,"median_working_seconds":900,"median_waiting_seconds":240,"median_response_seconds":75},"cost":{"metered_runs":3,"unmetered_runs":6,"cost_usd":4.12,"input_tokens":120000,"output_tokens":9000},"pull_requests":{"opened":2,"merged":1},"recent":[{"run_id":"...","title":"...","workspace_id":"...","status":"completed","reason":"","harness":"claude","model":"","created_at":"...","finished_at":"...","duration_seconds":1200,"mission_id":"","pr_url":"","pr_state":""}],"needs_you":2,"coverage":{"state_times_since":"...","pull_requests_since":""}}`; `Totals` is `{"launched","completed","merged","failed","interrupted","abandoned","killed","active","finished"}`. What each number counts is in [insights.md](insights.md) |
| `agent.list` | `{"account_member_id":"<member-id>"}` (optional; empty selects the caller's account) | `{"agents":[{"name":"claude","display_name":"Claude Code","glyph":"claude","source":"shipped"\|"member","installed":true,"executable":"claude","enhanced":"native"\|"adapter"\|"none","enhanced_installed":false,"switchable":true,"login_found":true,"default_mode":"tui"\|"acp","enhanced_default":false,"login_missing":false,"own_account_only":false,"unavailable":"","install_script":"...","enhanced_install_script":"..."}]}` - the caller's own shipped and member-defined agents, also for a shared account; `installed` reads the caller's `~/.local/bin`, and on a shared account is also true when only the owner's has the executable, whose installation a launch there then uses. `glyph` is the shipped name, or `custom` for a member-defined agent. `enhanced_installed`, `login_found`, `default_mode` and `enhanced_default` are described in [harnesses.md](harnesses.md#enhanced-mode-adapters); `executable` is the command the agent runs, which a member's agent may name differently from the agent; `login_found` is absent for an agent that declares no login path. `switchable` says whether `run.mode.switch` can move a running session of that agent between Standard and Enhanced. On a shared account, `login_missing` is true when a launch of that agent there is refused because its owner has no login for it (a missing or empty file), and `own_account_only` when it is refused because the name resolves to the caller's own member-defined agent, which runs only on the caller's own account, and `unavailable` carries the launch's own error when the owner's login exists but cannot be shared; at most one is set |
| `agent.models` | `{"name":"claude","account_member_id":"<member-id>","refresh":true}` (`account_member_id` and `refresh` optional) | `{"models":[{"id":"opus","name":"Opus 5.5","description":"..."}],"default_label":"Opus 5.5 · ...","source":"agent"\|"cache"\|"fallback"\|"none","fetched_at":"<RFC3339>","error":"..."}` - the models the agent's installed CLI offers on the account's login, cached for an hour; `refresh` discovers again. `error` is the last failed discovery's error, also when `models` holds an older or fallback list. A shared account that a launch would refuse answers with that refusal. See [harnesses.md](harnesses.md#model-discovery) |
| `agent.install` | `{"name":"claude","enhanced":true}` (`enhanced` optional) | `{"log_tail":"...","installed":true,"enhanced_installed":true,"error":"the install command exited 1"}` - runs a shipped agent's install command, and its adapter's with `enhanced`, in the caller's environment terminal, starting it if needed; `error` is set when the command failed. Needs the launch capability. Answers within 10 minutes; one install per member at a time, a second answers `-32003` |
| `agent.remove` | `{"name":"claude"}` | `{"removed":["~/.local/bin/claude","~/.local/bin/claude-agent-acp"]}` - removes an agent from the caller's own environment so new runs cannot start it. A shipped name (it wins over a member's definition of the same name; `custom` is not removable) deletes its `~/.local/bin` links, the Enhanced adapter's too; `removed` lists those that existed, and none existing answers `-32602` `agent "claude" is not installed in your environment: ~/.local/bin/claude does not exist`. Any other name deletes the caller's definition and answers `{}`, or `-32602` `you have no agent "x"; aether agent list shows yours`. `-32004` `persistent homes are not enabled` without member homes. Needs the launch capability; refused with `-32003` while an install or a harness update runs in the home. Package files, logins and settings stay. Not replayed on a transport failure |
| `files.tree` | `{"workspace_id":"...","run_id":"...","path":"src"}` (`run_id` optional; an empty, omitted or `"."` path is the root) | `{"entries":[{"name":"main.go","kind":"file","size":1234},...]}` |
| `files.read` | `{"workspace_id":"...","run_id":"...","path":"README.md"}` (`run_id` optional) | `{"content":"...","truncated":false,"binary":false,"size":1234,"revision":"<sha256>","writable":true}` |
| `files.write` | `{"workspace_id":"...","run_id":"...","path":"README.md","content":"...","revision":"<sha256>"}` (`run_id` optional; an empty `revision` creates a new file) | the same `FileRead` shape as `files.read`, for the saved bytes |
| `files.diff` | `{"run_id":"...","path":"README.md"}` | `{"patch":"...","truncated":false}` |
| `config.roots` | `{}` | `{"roots":[{"harness":"claude","display_name":"Claude Code","path":"~/.claude","runtime_ignores":["projects/","shell-snapshots/","statsig/","todos/","file-history/","history.jsonl","daemon/","transcripts/",...],"credential_names":[".credentials.json",...]}]}` |
| `config.tree` | `{"harness":"claude","path":"."}` (`path` may be omitted, empty, or `"."` for the root) | `{"entries":[{"name":"settings.json","kind":"file","size":1234},...]}` |
| `config.read` | `{"harness":"claude","path":"settings.json"}` | `{"content":"...","truncated":false,"binary":false,"size":1234,"revision":"<sha256>","writable":true}` |
| `config.stat` | `{"harness":"claude","paths":["settings.json","CLAUDE.md"]}` (at most 2,000 paths) | `{"entries":[{"path":"settings.json","size":1234,"revision":"<sha256>"}]}`: one entry per path that exists, with the path as it was sent; an absent path, and one the credential and runtime policy hides, has none. `revision` is empty for a file over 64 MiB, which is not read. `"blocked":true` replaces `size` and `revision` for a path `config.import` would refuse: a directory, a symlink, a hard link, or a path through one |
| `config.write` | `{"harness":"claude","path":"settings.json","content":"...","revision":"<sha256>"}` (`revision` is empty only for a new file) | the same `FileRead` shape as `config.read`, for the saved bytes |
| `config.import` | `{"harness":"claude","files":[{"path":"settings.json","content_base64":"...","mode":420}],"overwrite":false}` (`overwrite` optional) | complete: `{"harness":"claude","files":1,"bytes":12,"excluded":[{"path":"notes.md","reason":"secret","detail":"..."}],"replaced":["settings.json"]}`, `replaced` absent when nothing was replaced; an incomplete write after at least one file is committed adds `"error":"...failed relative path...: ..."` and `"imported_paths":["settings.json"]` |
| `home.tree` | `{"path":".claude"}` (`path` may be omitted, empty, or `"."` for the home itself) | `{"entries":[{"name":".credentials.json","kind":"file","size":412,"locked":true},{"name":"skills","kind":"dir","size":4096,"locked":false}]}` |
| `home.read` | `{"path":".claude/settings.json"}` | the same `FileRead` shape as `config.read` |
| `home.write` | `{"path":".claude/settings.json","content":"...","revision":"<sha256>"}` (`revision` is empty only for a new file) | the same `FileRead` shape, for the saved bytes |
| `home.mkdir` | `{"path":"notes/drafts"}` | empty result |
| `home.rename` | `{"from":"notes/todo.md","to":"notes/done.md"}` | empty result |
| `home.remove` | `{"path":"notes"}` | empty result |
| `home.upload` | `{"path":"notes/logo.png","content_base64":"...","overwrite":false}` | empty result |
| `home.download` | `{"path":"notes/logo.png"}` | `{"content_base64":"...","size":1234}` |
| `terminal.status` | none | `TerminalStatusResult` - whether the member environment is running, its `image`, optional `saved_image`, start time, and active tabs |
| `terminal.stop` | none | empty result; stops the member environment and its tabs |
| `terminal.exec` | `{"command":"claude --version"}` | `{"exit_code":0,"stdout":"...","stderr":"..."}` - runs the command through a login shell in the caller's member environment, starting it if needed; a non-zero exit code is a result, not an error. Any approved member may call it. Answers within 10 minutes: a command still running then is killed and the call answers `-32603`, as it does for output past 1 MiB. See [terminal.md](terminal.md#run-one-command) |
| `terminal.image` | `TerminalImageParams` (`{"run_id":"<run-id>","content":"<base64-original-image-bytes>"}`; `run_id` optional) | `TerminalUploadResult` (`{"path":"/home/<account>/.aether/terminal-images/image-<random>.png"}`) - absolute path in the target container |
| `terminal.file` | `TerminalFileParams` (`{"run_id":"<run-id>","name":"notes.md","content":"<base64-file-bytes>"}`; `run_id` optional) | `TerminalUploadResult` (`{"path":"/home/<account>/.aether/terminal-files/<random>/notes.md"}`) - absolute path in the target container |
| `env.save` | none | `EnvSaveResult` (`{"image":"aether/member-<id>:<unix-seconds>"}`) - commits the running environment terminal as the member's image |
| `env.reset` | none | empty result; stops the environment, forgets and removes the saved image |
| `assistant.status` | none | `AssistantStatus` - `state` (`setup`, `needs_restart`, `idle`, `starting`, `running`, `failed`), `agent`, each agent's readiness and reason, `error`, the pending `confirmations` (each with `other_member` and `other_owner`, the owner's name, when the run belongs to another member), `auto_approve`, `suggest_auto_approve` and `share_view`. `needs_restart` means the environment was created before the assistant existed and lacks its mounts |
| `assistant.start` | `{"agent":"claude","restart_environment":false}` (both optional) | `AssistantStatus` - starts the caller's assistant, an Enhanced session of their own agent in their environment, resuming the stored session. `agent` defaults to the stored agent, which is never replaced unasked: when it is not ready the call fails with `<Agent> is not ready: <reason>; sign in again or start with another agent` (`-32004`). With no stored agent it picks the first ready of `claude`, `codex`. Naming a different agent begins a new conversation. `restart_environment` stops and recreates the environment first |
| `assistant.prompt` | `{"text":"...","idempotency_key":"...","view_context":{...}}` (`view_context` optional) | empty result; needs the Launch capability, starts the assistant when needed, and ignores a key it already accepted. While a turn runs it answers `-32002`: cancel the turn first. `view_context` is `{"version":1,"captured_at":"...","route":{"name":"run","run_id":"...","view":"changes"},"open_file":{"kind":"workspace","path":"..."},"recent":[]}`; malformed ids and paths are dropped, `recent` is cut to 8 pages, the oldest pages and then `open_file` go until it fits 4096 bytes, and an unknown `version` is dropped. Unless `share_view` is off, it reaches the agent as an `aether://view-context` resource and becomes the caller's latest view context |
| `assistant.view` | `{"view_context":{...}}` | empty result; replaces the caller's latest view context, which the assistant reads with `aether_view_context`, unless `share_view` is off. The last one received wins. Kept in memory only |
| `assistant.cancel` | none | empty result; cancels the turn in flight |
| `assistant.answer` | `{"request_id":"...","option_id":"...","values":{}}` | empty result; answers one of the harness's permission prompts or questions |
| `assistant.confirm` | `{"confirmation_id":"...","approve":true}` | empty result; answers a tool call waiting for approval. A confirmation expires after 10 minutes |
| `assistant.reset` | none | `AssistantStatus` - stops the session and discards the conversation |
| `assistant.settings` | `{"auto_approve":true,"share_view":false}`, `{"suggestion":"later"}` or `{"suggestion":"never"}` (each optional; omitted reads) | `AssistantSettings` (`{"auto_approve":true,"suggest_auto_approve":false,"suggestion_off":false,"share_view":false}`) - the caller's own assistant settings. With `auto_approve` on, the assistant's confirmed tools run without a card, except on another member's run. `suggestion` answers the [suggestion](assistant.md#the-suggestion) to turn it on; `suggestion_off` means it will not show again. `share_view` (default on) lets prompts and `assistant.view` carry the caller's view context; off forgets the one held. The assistant's tool socket does not serve this method |
| `assistant.history` | `{"before_seq":0,"limit":1000}` | like `run.acp.history` |
| `assistant.item` | `{"seq":12}` | like `run.acp.item` |
| `workspace.origin` | `WorkspaceOriginParams` (`{"workspace_id":"...","origin":"https://github.com/acme/app.git"}`; `origin` empty clears it) | `WorkspaceOriginResult` - the workspace with its new `origin`, the upstream every new run checkout's `origin` remote points at |
| `workspace.environment.get` | `WorkspaceEnvironmentGetParams` (`{"workspace_id":"..."}`) | Any member. `WorkspaceEnvironmentResult` (`{"workspace_id":"...","setup_script":"npm ci\n","variables":[{"name":"API_URL","value":"https://api.example.test"},{"name":"NPM_TOKEN","secret":true}]}`), variables sorted by name; a secret never carries `value` |
| `workspace.environment.set` | `WorkspaceEnvironmentSetParams` (`{"workspace_id":"...","setup_script":"npm ci\n","set":[{"name":"NPM_TOKEN","value":"...","secret":true}],"unset":["OLD_NAME"]}`; every field but `workspace_id` optional). Admin only. `set` adds or replaces, `unset` removes, a present `setup_script` replaces the script and `""` clears it; whatever is not named is kept. The script and each value are limited to 64 KiB | `WorkspaceEnvironmentResult` after the change, with `warning` when the change is saved and its timeline note was not recorded. A `run.launch` or `template.launch` the setup script fails answers with error data `{"setup_output":"..."}`: the end of what the script printed, secret values masked |
| `workspace.mirror.status` | `WorkspaceMirrorParams` (`{"workspace_id":"..."}`) | Read-only for admitted members. `WorkspaceMirrorResult` reports whether mirroring is enabled, source, branch, status, observed and accepted commits, check times, public key, and safe warning/error fields; no private key or server path |
| `workspace.mirror.configure` | `WorkspaceMirrorConfigureParams` (`{"workspace_id":"...","source_url":"https://github.com/acme/app.git","branch":"main","auth":"public"\|"deploy-key"\|"github","known_hosts":"...","github_account_id":42}`; last two fields optional and mode-specific) | `WorkspaceMirrorResult`; deploy-key configuration includes only the public key and safe installation warning; GitHub mode binds the authenticated admin and verified numeric account |
| `workspace.mirror.refresh` | `WorkspaceMirrorParams` (`{"workspace_id":"..."}`) | `WorkspaceMirrorResult` after fetching the configured source branch |
| `workspace.mirror.adopt` | `WorkspaceMirrorAdoptParams` (`{"workspace_id":"...","generation":7,"expected_commit":"<full-reviewed-sha>"}`; all fields required) | `WorkspaceMirrorResult` after explicitly accepting exactly the reviewed retained candidate |
| `workspace.mirror.disable` | `WorkspaceMirrorParams` (`{"workspace_id":"..."}`) | `WorkspaceMirrorResult` with `enabled:false`; the workspace becomes local-only |
| `github.connect` | none | `GitHubConnectResult` (`{"login":"...","signing_key":"ssh-ed25519 ...","fingerprint":"SHA256:..."}`) - finishes the GitHub connection for the calling member |
| `github.probe` | none | `GitHubProbeResult` (`{"status":"ok","version":"2.100.0","minimum":"2.81.0","detail":"gh version 2.100.0 (2026-09-03)\nhttps://github.com/cli/cli/releases/tag/v2.100.0","image":"ghcr.io/3xdevops/aether-standard:v0.2.0-alpha.7"}`) - the gh in the calling member's environment terminal |
| `github.oauth.start` | `{}` | `GitHubOAuthResult`; admin-only, starts or returns the calling member's active authorization attempt |
| `github.oauth.status` | `{"session_id":"..."}` (optional) | `GitHubOAuthResult`; admin-only, read-only connection/attempt inspection |
| `github.oauth.cancel` | `{"session_id":"..."}` (required) | `GitHubOAuthResult`; admin-only, cancels only that member's exact current attempt |
| `github.repositories.list` | `{"page":1}` (optional) | `GitHubRepositoryListResult`; admin-only, repositories readable by the calling member's native GitHub account |

Mirror adoption pins both the configuration `generation` and the candidate's
full `observed_commit` as `expected_commit` (SHA-1 or SHA-256, not a revision
expression or abbreviated hash). Generation-only clients must migrate: missing
or malformed `expected_commit` returns invalid params (`-32602`). If a refresh
changes the candidate, even within the same generation, adoption returns conflict
(`-32003`) without moving the accepted/base refs or discarding the current candidate.
Keep the workspace, read fresh status, and explicitly review that candidate before
submitting its generation and commit again. Never fetch a new SHA automatically
while submitting or replay a failed adoption with a substituted SHA.

### Administrator GitHub connection and import

Both gateways expose the same administrator flow; it needs no local clone or
loopback OAuth callback. The assembled server wires `Services.GitHubOAuth`
directly to the scheduler, independently of run-control decorators.
The authenticated transport supplies the member; none of these methods accepts
a credential owner selected by the browser. Existing `github.connect` and
`github.probe` remain member-scoped for native terminal setup.

`GitHubOAuthResult` has required `state` and optional `session_id`, `user_code`,
`verification_url`, `expires_at` (timestamp), `login`, `connection` and `error`.
`connection`, when present, is the existing `GitHubConnectResult` with `login`,
`signing_key` and `fingerprint`. No token or secret device code is returned.

| State | Meaning |
| --- | --- |
| `disconnected` | No usable current native connection was found; status does not start an Environment to discover one |
| `starting` | Preparing the Environment and native authorization |
| `pending` | Waiting for approval; show the one-time `user_code` and `https://github.com/login/device` |
| `finishing` | Completing native credential/signing setup after approval |
| `connected` | Native connection available; a successful start has also completed signing setup |
| `expired` | The bounded authorization attempt expired; explicitly start again |
| `failed` | Authorization, inspection or setup failed; display `error` |
| `cancelled` | The attempt was stopped; explicitly start again if desired |

Start ensures the member's Environment exists and runs native `gh` without a
visible terminal. Only one active attempt exists per member; repeated starts
return it. Attempts last at most 15 minutes. Cancel, expiry, Environment stop
and scheduler shutdown stop the authorization process. Cancel is not logout.
An explicit stale or another member's `session_id` is rejected, not applied to
the latest attempt. Omit `session_id` on a fresh visit to inspect the current
connection/attempt. Status never initiates login, writes Git config, registers
keys or starts an Environment; completed success is rechecked against native
login rather than trusted forever. Poll only active states and do not replay
start/cancel after a lost response: inspect status first.

Missing signing permission or a known failed setup remains non-connected.
Status does not retry setup; an explicit start retries completion or requests
authorization again.

Repository listing returns
`{account:{id:number,login:string},repositories:[{id:number,full_name:string,name:string,private:boolean,default_branch:string,clone_url:string,can_push:boolean}],next_page?:number}`.
Omitted or zero `page` means 1; negative pages are invalid. Pages contain at most
100 repositories, including readable collaborator and organization repositories,
not just owned repositories. Request `next_page` while present. `can_push:false`
does not prevent importing a private source. An empty list is not an
authentication failure; a repository without a default branch needs an initial
commit before import.

`workspace.import` remains the creation API:
`{name,environment,source_url,base_branch,origin,auth,known_hosts?,github_account_id?}`.
`auth` is `public`, `deploy-key` or `github`. For GitHub, use credential-free
`https://github.com/owner/repo.git`; `base_branch` may be empty to use the
verified default branch. Send the selected list's numeric `account.id` as
`github_account_id` (optional on the wire; zero means unspecified). A supplied
positive ID must match the verified account; it is not a login name or credential
owner. The server re-reads repository access/identity, derives the member/account
binding, ensures the Environment and completes native GitHub setup before
creation. It requires read access, not repository-admin or push permission.
`origin` is explicitly chosen or empty: the source never implicitly becomes a
publish destination. `known_hosts` belongs to SSH deploy-key configuration, not
GitHub mode; `github_account_id` is rejected for other authentication modes.

The result is `{workspace,created,mirror,error?}`. Once created, downstream
configuration/fetch failures return `created:true` and the workspace ID; keep it
and repair/refresh its source, never repeat creation. Fetch observes a candidate;
only `workspace.mirror.adopt` with the reviewed `generation` and full
`observed_commit` supplied as `expected_commit` accepts it.
A lost import response is uncertain: inspect Workspaces before retrying.
GitHub mirror status exposes non-secret `github_member_id` and `github_user_id`
alongside `auth`, source, branch, generation and observed/accepted revisions.
Refresh resolves the bound member's credential and verifies the numeric account
again; revoked/missing/changed credentials fail without discarding the accepted
base. Explicit `workspace.mirror.configure` with `auth:"github"` deliberately
rebinds to the current admin's verified account (and can check
`github_account_id`); it cannot select another member's credential.


### Files and member configuration

`files.tree` and `files.read` address a workspace's base branch when
`run_id` is omitted, or a live run checkout when it is present. Complete
valid UTF-8 text without NUL bytes is returned with an exact SHA-256
`revision` and may be writable. Binary content is read-only with an empty
revision. File reads, diffs, and editor writes carry complete content up to 64
MiB. Binary or oversized content is read-only; an oversized response sets
`truncated:true` and omits its `revision`. The `truncated` field remains in
the wire shape for compatibility.

`files.write` without `run_id` requires **Push** and creates a one-file commit
on the workspace base branch; it does not push upstream. With `run_id`, it
requires **Steer** and changes that run's uncommitted checkout. Existing file
modes are preserved and new files use `0644`. Run-checkout saves recheck the
revision immediately before atomic rename under Aether's root lock. Base
commits compare-and-swap the branch head. Aether's locks do not serialize
arbitrary live-agent filesystem writers.

`config.roots`, `config.tree`, `config.read`, `config.stat`, `config.write`,
and `config.import` always address the authenticated member's own persistent
home.
They require **Launch**; an administrator cannot select another member with an
extra request field. `config.write` has the same explicit-save and revision
rules as `files.write`. `config.import` writes a batch of files into one
agent's directory in that home; `aether config push` calls it over the SSH
control channel ([Upload agent configuration](quickstart.md#upload-agent-configuration)).
All runs the member launches and the environment terminal mount one shared
read-write persistent HOME; an account share exposes only the agent login
from it, except that an `omp` share exposes the whole `~/.omp/agent`, and a
recipient with no installation of the agent also gets the owner's
installation read-only ([security.md](security.md#account-sharing)). A file edit, configuration upload, or
manual CLI profile operation is therefore visible to active and future runs,
although a tool may need to reload its configuration.
Configuration saves and imports bind inherited permissions to the observed
destination inode and mode. After staging, they recheck that identity and mode
immediately before atomic rename; an absent destination must still be absent,
and editor saves also recheck the content revision. A detected change returns
`config: conflict` without replacing that destination. Staged bytes remain
private until the destination permissions have been validated.
These checks are optimistic, not a filesystem compare-and-swap: Aether's root
lock coordinates its own operations, not arbitrary processes in the shared
HOME, which can still write between the final check and rename.

`home.tree`, `home.read`, `home.write`, `home.mkdir`, `home.rename`,
`home.remove`, `home.upload`, and `home.download` address any path in that
same home, not one agent's directory. The dashboard's **Files** view calls
them for its **Home** tree. They require **Launch** and take no member
selector. Paths are slash-separated and relative to the home; an absolute
path, a `..` or empty segment, a backslash, or a NUL byte is refused.

| Method | Does |
| --- | --- |
| `home.tree` | Lists the immediate children of a directory, sorted by name, with nothing left out: dotfiles, agent runtime folders, and credential files are listed. `kind` is `dir`, `file`, or `link` for a symlink; sockets, FIFOs, and devices are omitted. `locked` marks a credential file. |
| `home.read` | Reads one file with the rules and result of `config.read`. |
| `home.write` | Saves complete UTF-8 text with the revision rules of `config.write`. An empty `revision` creates the file as `0644`, with any missing parent directories, and refuses an existing one. |
| `home.mkdir` | Creates one directory, `0700`. Its parent must exist and the name must be free. |
| `home.rename` | Renames or moves a file, directory, or symlink. It never replaces: an existing `to` is refused, and so is moving a directory into itself. A credential file, and `.ssh`, `.config`, or `.config/gh`, is refused. |
| `home.remove` | Deletes a file or symlink, or a directory with everything in it. The home itself is refused. |
| `home.upload` | Writes one file of arbitrary bytes up to 64 MiB, creating missing parent directories. An existing file is replaced only with `"overwrite":true` and keeps its mode; a new file is `0644`. |
| `home.download` | Returns one regular file up to 64 MiB as base64. |

A credential file is listed and never served. `home.read`, `home.download`,
and `home.write` refuse it with `-32001` and a message such as
`.claude/.credentials.json is a credential file; read it in the Environment terminal`.
A path is a credential file when any of its components is one of the
`credential_names` of a shipped agent, matched case-insensitively, or ends in
`.pem` or is `.env` or `.env.*`; when it is under `.ssh/` and does not end
in `.pub`; or when it is
`.config/gh/hosts.yml`, `.git-credentials`, or `.netrc`. `home.rename`
refuses a credential file, and a folder holding one of those fixed paths,
with the same code, since a renamed copy would be readable. `home.remove`
and `home.upload` work on a credential file. The lock keeps credential bytes
out of the browser and is not a boundary against the member, whose terminal
reads the same home
([security.md](security.md#agent-configuration-and-files)).

No `home.*` method follows a symlink. A symlink is listed, renamed, and
removed as itself; a read, write, upload, or download of one, or of anything
under a symlinked directory, is refused, as is a regular file with more than
one hard link. New files and directories are owned like the home. The
credential, runtime-ignore, and secret-scanner exclusions of `config.import`
do not apply: they decide what `aether config push` uploads, not what a
member may see or change in their own home. One lock per member serializes
`home.*` and `config.*` calls and profile materialization, so a Files save
and an `aether config push` of the same file cannot both pass their revision
check; like configuration saves, they are not atomic against other processes
writing the home.

`home.*` error messages carry no method prefix:

| Code | When | Example message |
| --- | --- | --- |
| `-32001` | A credential file, a symlink or hard link, or an invalid path | `notes cannot be moved into itself` |
| `-32000` | The path, or the parent a new entry needs, does not exist | `notes/gone was not found` |
| `-32003` | The name is taken, or the file changed since `revision` was read | `notes/logo.png already exists` |
| `-32602` | A file over 64 MiB, or text to save that is binary or not UTF-8 | `big.bin is over the 64 MiB limit` |
| `-32004` | The operating system refused | `permission denied` |

`home.write` shares the HTTP body limit of `config.write`, and `home.upload`
that of `config.import`, both below; `home.upload` does not use import
admission.

`config.roots` returns each agent's `runtime_ignores` and
`credential_names`. `runtime_ignores` contains case-sensitive patterns
anchored at the agent's directory; one matches a path and everything below
it, `*` matches any run of characters within one name, and trailing slashes
are ignored for matching. These lists are per agent, so a runtime file
ignored for Claude is not implicitly ignored for OMP or a member-defined
custom agent. Known credential names wherever they occur in a path, and every
basename that is `.env` or starts with `.env.` or ends in `.pem`, remain
filtered by the destination-independent credential policy.
`aether config push` applies both lists before reading a file and runs the
secret scanner locally, so flagged bytes are not sent; the server applies
the same policy and scanner again.

`config.stat` gives `aether config push` the size and revision of the
server's copy of each file it is about to send, without reading the content
back. The CLI sends only files that are absent there, or, with
`--overwrite`, that differ. `config.import` enforces the same rule on its
own: a file whose destination already holds the same bytes is excluded with
reason `identical`, and one whose destination holds different bytes is
excluded with reason `differs` unless the request sets `overwrite`. A client
that predates `overwrite` therefore cannot replace a file.

Each `config.import` request permits at most 2,000 files and 64 MiB decoded,
with a 64 MiB per-file ceiling matching configuration editing. These bounds
limit a request, not a directory: `aether config push` sends batches of at
most 2,000 files and a target of 20 MiB decoded, and a larger file alone.
Paths are slash-separated and relative to the agent's directory. `mode`
carries permission bits for a new file, and `0` means `0644`; a replaced
file keeps its existing remote mode. Empty and binary regular files are
preserved. The server rejects unsafe paths, symlink components, hardlinks,
and non-regular destinations.

The result's `files` and `bytes` count
written files only, and `replaced` names the ones that took the place of
different bytes; `excluded` reports server-side credential, ignore, secret,
identical, and differs exclusions. Explicit CLI profile `push`, `status`, and
`rollback` remain separate manual operations; neither `config.import` nor
**Files** edits create CLI snapshot history. The persistent HOME does not
depend on snapshots. Snapshot pins record optional launch provenance, not
isolated writable run copies. Manual push and rollback overlay snapshot files
into the same HOME without deleting files absent from the snapshot; rollback
is not an exact-tree restore. There is no directory watcher, automatic
configuration synchronization, or automatic import retry.

If an import fails after writing one or more files, the response is still a
`config.import` result rather than a JSON-RPC failure. Its `files` and `bytes`
are the exact committed counts, `imported_paths` lists the exact canonical
paths that remain in the member's shared HOME, and `error` names the failed
relative path and the underlying error. This is an incomplete import, not a
successful one: `aether config push` prints those paths as `created` or
`replaced`, sends no further batch, and exits with the error. The optional `error` and `imported_paths`
fields are absent from complete results and from failures before any file is
committed.

If the SSH/RPC call fails without such a result, the outcome is unknown: the
request may have copied some files before the response was lost.
`aether config push` prints that request's paths as `unconfirmed`, sends no
further batch, and exits with the error; `config.tree` shows what the home
holds. Earlier confirmed batches remain reported. Cancellation can prevent
the response from being delivered; the protocol does not claim a stronger
delivery guarantee.

The generic HTTP proxy caps ordinary `/api/v1` JSON bodies at 1 MiB,
`files.write` and `config.write` at 385 MiB to allow worst-case JSON escaping,
and `config.import` and `terminal.file` at 96 MiB. The 64 MiB file and
per-request import limits remain authoritative after JSON decoding. The SSH control-channel line cap is
96 MiB, allowing base64 import requests without unbounded framing.
The HTTP gateway admits at most two imports at once, before reading their
bodies, and retains admission through backend processing and the response.
Excess requests receive HTTP 503 (`configuration import capacity is busy;
retry later`) without being read. An import body must make progress within
30 seconds and finish within 15 minutes; expired reads return HTTP 408.
These are transfer protections, not limits on a directory.
SSH frames exceeding 64 KiB require a separate server admission: two globally
and one per member, retained through the response. Small control requests
remain available. Partial-frame reads have a 30-second idle timeout and a
15-minute total timeout. Expanded frames retain that total timeout through
dispatch and response; complete small requests do not. Expiry closes the
offending SSH connection, including its other channels. Idle channels between
requests do not start these timers.

### `GET /api/v1/run/<run_id>/patch`

Not an RPC method, because patch text is a read of a working tree rather
than a control-channel call. `run.diff` events carry per-file stats and the
tree each snapshot wrote, never patch text, and no patch text is stored
anywhere in the server, so the diff timeline reads the text here and uses
those events to know when to ask again and which snapshot to ask from.

With no query params the server renders the run checkout's whole diff against
the run's effective base, covering committed work, uncommitted edits and
untracked files alike - the same set of changes a `run.diff` snapshot counts.
The effective base is the newest upstream commit the run's `HEAD` contains:
the merge base of `HEAD` and the checkout's `refs/remotes/origin/<base
branch>`, which `git fetch` or `git pull` inside the run updates. A run that
merges or rebases onto newer upstream work is therefore charged only for its
own changes, as in a GitHub pull request. The base never moves behind the
fork point (the `aether.base` commit its identity record pins): when the
tracking ref is missing, unreadable, or not a descendant of the fork point,
or the checkout predates the recorded base branch, the fork point is the base.
The server reads the tracking ref as plain data and never runs Git against the
checkout's own repository. Rendering leaves the checkout alone: the
worktree is staged into a scratch index with its own scratch object directory,
so nothing under the checkout's `.git` - not even the loose objects staging
hashes - is ever written, and the scratch files are deleted once the patch is
rendered.

Every server read of a checkout - stats, patch, snapshot, evidence, and the
staging behind an `aether:` or `wip:` commit - uses the server's own Git
configuration and the `.gitattributes` of the fork point, never the copies in
the worktree. A file the fork point declares `eol=crlf` is therefore compared
after the same line-ending conversion that checked it out. A linked worktree
(`git worktree add`) inside the checkout is another checkout of the
repository, not a change: its directory is left out of all of them and of
`files.tree`, whatever the checkout's ignore rules say.

A `run.diff` stat set lists every changed path, each with a `status` of
`added`, `deleted` or `modified`, but reads at most 128 MiB of
untracked content to count lines. Past that the event carries
`truncated:true`: the files left unread count zero and the totals are a
floor.

`from` alone renders everything that changed since a recorded snapshot: the
diff from that snapshot's `tree`, off a `run.diff` event, to the run's current
state. With a checkout the worktree is staged exactly as for the whole diff,
so edits made after the snapshot count, committed or not, and the base is the
`from` tree. After checkout cleanup the diff runs from `from` to the last
recorded snapshot and carries `recorded:true`. `fork_tree`, on every render
without `from`, is the tree of the fork-point commit; passing it as `from`
renders the run's current state against where it started, including upstream
work merged since.

`from` and `to` together render one interval: the diff between the two trees,
which is what the run changed in that interval. `to` without `from` is an
invalid-params error. An id has to be a full object id, has to be recorded in
that run's own snapshot store and no other, and has to name a tree: a commit
id would otherwise peel to its tree and render a diff the timeline never
offered. The fork-point tree counts as recorded.

Snapshot sidecars preserve captured trees until explicit run deletion, even
after checkout cleanup. Consecutive unchanged trees do not add duplicate refs.
Range reads and evidence imports share the snapshot lock with explicit
deletion. Published interval refs change atomically, and in-flight pins protect
delivered endpoints across ref or event-publication failures.
Watch snapshots and cumulative-diff staging bound visible input at 128 MiB
(evidence keeps its 64 MiB input bound); these limits do not evict history.
A cumulative response after checkout cleanup is the last recorded snapshot,
not a live filesystem read, and carries `recorded:true`. It is measured
against the tree of the effective base that snapshot's stats used, which the
snapshot store records in the same ref transaction that publishes the
snapshot, so the pair never splits. Stopping a run's watcher, as run
completion does, takes one last snapshot first, so the final `run.diff` and
the recorded diff include changes made in the seconds before the stop.

Each `run.diff` carries `base`, the effective base commit its stats are
measured against. A fetch that moves `origin/<base branch>` schedules a
snapshot, and a moved base publishes new stats even when the worktree is
unchanged, including after a watch restart. `parent_tree` is unchanged: the
first snapshot's parent is the fork-point tree. It also carries `head`, the
commit the checkout had checked out, and `head_subject`, that commit's subject
line cut to 200 bytes. A `head` that differs from the previous snapshot's means
a commit landed between the two. The subject is read by the server from its
own object store, never by running Git in the checkout.

`run.diff` may include `history_gap:true` and a bounded, path-free
`snapshot_error` when an interval could not be recorded. The next successful
interval also advertises a gap if it spans missed work. Clients must not show
an ordinary complete interval for these events. The current cumulative diff
remains independently usable within its staging bounds.
Gap publication retries preserve that distinction and may deliver metadata
at least once; they never replay an agent action or tool invocation.
Staging refusal returns unavailable with
`run.patch: current diff input exceeds 128 MiB or available disk headroom`;
`files.diff` uses the same explanation with its own method name. It does not
claim the checkout was removed, and it never returns a partial complete diff.

```json
{"run_id":"run_01H...","base":"9f2c1e...","patch":"diff --git a/main.go b/main.go\n...","truncated":false,"fork_tree":"4b825d..."}
```

- Visibility is `run.get`'s, applied by calling it: a member who could not
  read the run over the control channel gets that method's refusal here,
  unchanged.
- `base` is what the patch is measured from: the effective base commit on a
  live cumulative render, its tree when `recorded` is true, the `from` tree on
  any render that names one.
- `fork_tree` is the fork-point commit's tree, present on renders without
  `from`.
- `patch` contains the complete rendered diff up to 64 MiB. An oversized
  response sets `truncated:true` and ends at a whole line. The dashboard
  renders the response inline; it does not serve repositories.
- `503` with `-32004` when the server has no git engine wired, when the run
  has no checkout to diff (`run.patch: this run has no checkout to diff`),
  when a requested tree has expired from retained snapshot history (possibly
  before checkout cleanup), or when rendering ran past the engine's 30s
  ceiling (`run.patch: rendering the diff took longer than 30s: ...`) - the
  same bound a diff snapshot's git work gets, because staging re-hashes every
  untracked file and a worktree holding a large un-ignored tree would
  otherwise be unbounded. Any other failure returns `run.patch:` followed by
  the engine's error, including the Git command and its output.

### `GET /api/v1/disk`

Usage of the filesystem holding the server's data directory and storage
attribution, shown in **Settings > Server**:

```json
{"used_bytes":21474836480,"total_bytes":107374182400,"free_bytes":85899345920,
 "worktree_bytes":3221225472,"transcript_bytes":104857600,"database_bytes":52428800,
 "repo_bytes":8589934592,"home_bytes":1073741824,"evidence_bytes":20971520,
 "scratch_bytes":0,"cache_bytes":1073741824,"other_bytes":1048576,"snapshot_bytes":104857600}
```

`used_bytes` and `total_bytes` describe the whole filesystem - the gauge
answers "is the disk filling up", which is not a question about Aether's own
footprint. `free_bytes` is what an unprivileged writer can still claim, which
is the number the scheduler's free-space floor is checked against, and is
smaller than `total - used` wherever the filesystem reserves blocks.

The categories describe Aether's footprint: worktrees, transcripts, the
SQLite database (including persisted events), bare workspace repositories,
member homes, managed package/build caches, evidence, and other data-directory contents.
`snapshot_bytes` is a subset of `worktree_bytes`, never an additional total.
The categories are not a promise that their bytes can be reclaimed.

Directory accounting does not follow symlinks. It indexes file identities on
Linux, macOS and Windows to count hardlinks once across categories without
comparing every file with the rest of the scan. Shared Git objects are charged
to repositories before checkouts. Unreadable
data produces `warnings`; affected measurements are partial, not evidence
of an empty directory. Measurement lives in `internal/disk`, shared with
the scheduler's filesystem floor.

Optional `docker` accounting describes the whole daemon, including non-Aether
workloads: `images_bytes`, `containers_bytes`, `volumes_bytes`,
`build_cache_bytes` and `reclaimable_bytes`. Shared image layers count once;
build-cache bytes exclude image-shared records, and bind-backed homes are not
counted again as Docker volumes. Docker's unused classification does not mean
Aether can safely delete a saved environment image.

Optional `used_bytes`, `total_bytes`, `free_bytes` and `shared_filesystem`
require a verified local daemon whose measured storage roots share a
filesystem. Classic layer directories and containerd content/snapshotter
roots are checked separately; different filesystems have no single aggregate.
Filesystem used totals must not be added together. An omitted measurement is
unknown, not zero.
`docker.error` retains diagnostic causes with private paths and credentials
redacted, without discarding available data-directory measurements. This
endpoint never prunes Docker resources.

`containers` lists what each Aether container holds in its own writable
layer: files written outside its mounted home, checkout and cache, such as
`/tmp`. They are deleted with the container, live outside the data directory,
and are not part of any total above. Each item has `owner_kind` (`run`, or
`member` for that member's environment), `owner_id` and `bytes`, largest
first. Admins receive every container; other members receive their own runs
and environment. Docker walks each layer to answer, so the server measures in
the background at most every five minutes: `containers_measured_at` (ISO 8601)
is absent until the first measurement completes, and `containers_error` carries
a failed measurement while the previous sizes stay.

Admins additionally receive an optional `entries` array, bounded to the
largest 50 measured owners; `truncated` indicates clipping. Each entry has
`kind`, `owner_kind` (`run`, `member`, `workspace` or `server`), optional
`owner_id`, `bytes` and `reason`, with optional `reclaimable_bytes`,
`retained_until` (ISO 8601), `pool` and `error`. Cache pools are `runs` or
`terminal`, owned by the immutable launcher rather than a shared login account.
Their deadline is an age target: byte pressure may reclaim eligible inactive
caches earlier. Entries attribute the category
totals; they must not be added to them. Retention reasons use live lifecycle
ownership when present and durable state otherwise. Transcripts follow
evidence-protected checkout cleanup; expiration alone never proves safe
reclamation.
Unknown reclaimability is omitted. Inventory does not expose private
filenames or credentials; non-admin members receive aggregate accounting
without owner details.

Headroom (`used_bytes`, `total_bytes`, `free_bytes`) is read on every call.
The categories, `entries`, `warnings` and `docker` come from the last
finished walk of the data directory. A call that finds that walk more than
5 minutes old starts the next walk in the background and still answers from
the old one; one walk runs at a time. Until the first walk after a server
start finishes, a call waits up to 5 seconds for it, then answers `503` with
`-32004`: `server.disk: disk usage is still being measured: the server's
first scan of its data directory has not finished`.

`protocol.ServerInfoResult` is shared with the CLI and frozen, and the number
is of no use to a terminal client, so it is read here instead of being added
to `server.info`. Any member may read the aggregate deployment capacity.
Owner details are admin-only. `503` with `-32004` when the
server was not told where the data directory is, the platform has no
`statfs` (the server ships for linux; the read refuses rather than reporting
zero anywhere else), or the first walk is still running.




`terminal.image` accepts the original image bytes as strict base64 in
`content`; the server decodes and validates the bytes again. Only PNG, JPEG,
GIF, and WebP are accepted, and the decoded image must be non-empty and at
most 8 MiB. Invalid base64, an empty value, an unsupported or invalid image,
and an image over the decoded limit remain `-32602` invalid-params errors with
the validation message (for example, `image content is required`, `image
content is not valid base64: ...`, `image exceeds the 8 MiB limit`, or
`unsupported or invalid image format`).
An HTTP body over the 12 MiB image cap is still rejected before dispatch as a
`400` parse error; it is not passed to the control channel.

Omit `run_id` to target the authenticated caller's live environment terminal;
that terminal must already be running. Supplying `run_id` targets that run's
live supervised container and requires the caller's `Steer` capability. The
server stores the generated file in the persistent member home that container
mounts (the run's launcher's, also on a shared account) and returns its
absolute path as mounted at `$HOME` there.
The path and filename are server-generated; callers cannot choose a host path
or ask this method to read an arbitrary path.

`terminal.file` takes the same `run_id` and authorization for any file: strict
base64 `content` of at most 64 MiB decoded, empty allowed, and the original
`name`. The server writes it as `.aether/terminal-files/<random>/<name>` in
the same home, with `/`, `\` and unprintable characters in `name` replaced by
`_`, leading dots and spaces dropped, a name over 128 bytes cut keeping its
extension, and an empty name stored as `file`. Invalid base64 and an oversized
file are `-32602` with `file content is not valid base64: ...` or `file
exceeds the 64 MiB limit`.

- `run.patch` returns the complete rendered diff up to 64 MiB. An oversized
  response sets `truncated:true` and ends at a whole line.
  `from` and `to` select a starting snapshot or one interval here exactly as
  they do on the `GET`.
- The read methods answer `-32004` (unavailable) when the read cannot be
  served: `run.patch` when diff rendering is not enabled (no git engine
  wired), when the run has no checkout left to diff, or when a requested tree
  is gone (`run.patch: retained snapshot history has expired or is unavailable`),
  `server.disk` when the server was not told where the data directory is,
  the filesystem holding it could not be read, or its first walk of the data
  directory is still running, and `files.tree`,
  `files.read`, or `files.diff` when their checkout or repository is
  unavailable. All three files methods also answer `-32602` for a rejected
  path. The underlying errors name server-side paths,
  so they are not echoed to the client.
- `github.probe` is what a caller asks before it prints the login command.
  `status` is `ok`, `missing` (nothing named gh on `PATH`, exit 127),
  `broken` (gh is there and exited non-zero, including the 126 of a file
  that will not run) or `outdated` (older than `minimum`, the
  oldest release whose `gh auth status --json` the login check can read).
  `image` is the image that container is running and `saved_image` the
  member's own saved one; `version` is the release gh printed and `detail`
  what gh, or the container that could not run it, printed - prefixed with
  `gh --version exited <code>` when the status is `broken`, and the output
  after it when there was any. A gh that
  cannot do the login also carries `remedy`, the command the member runs,
  and `admin_remedy`, what a server admin has to run first when the
  server's own standard image is the one without a usable gh:

  ```json
  {"status":"missing","minimum":"2.81.0","detail":"OCI runtime exec failed: exec failed: unable to start container process: exec: \"gh\": executable file not found in $PATH","image":"ghcr.io/3xdevops/aether-standard:v0.2.0-alpha.5","remedy":"aether terminal stop","admin_remedy":"aether server update"}
  ```

  `status`, `minimum` and `image` are always there; `version`, `detail`,
  `saved_image`, `path`, `remedy` and `admin_remedy` are omitted when
  empty.
  `path` is present only when the gh that answered is a file inside the
  caller's own environment home, which comes first on `PATH` and outlives
  every image - so when it is set, no image remedy is sent with it.
  `admin_remedy` appears only when the standard image is the one at fault,
  which is never a member who has saved an environment, so it and
  `saved_image` never appear together.
- Like `github.connect`, `github.probe` runs `gh --version` in the
  container the member already has. It answers `-32002` (invalid state)
  when the terminal is not running, and for a container that goes away
  under it, rather than opening one: opening a container, or even resolving
  an image, can pull, and this gateway drops a control round-trip that
  takes more than sixty seconds. The exec itself failing answers `-32603`.
  The probe takes no terminal lock and gives up after twenty seconds, well
  inside that.
- `github.connect` is member-scoped and takes no parameters: it acts on
  the calling member's own environment terminal. It runs `gh` inside that
  container, so it answers `-32002` (invalid state) when the terminal is
  not running, when that container has no gh, has one that will not run,
  or has one older than `minimum` above, and when gh is not logged in to
  `github.com` there. The gh refusals come before the login is asked about
  at all, and carry the same way out the probe reports, though as one
  sentence rather than the separate `remedy` and `admin_remedy` fields;
  the login refusal ends with gh's own reason for the account, such as
  `HTTP 401: Bad credentials`, so the dashboard and the CLI can show what
  gh said rather than a summary. `signing_key` is the public key line and
  `fingerprint` its `SHA256:` fingerprint; the private key never leaves
  the server. The member-scoped terminal setup on Agents and the collaborator's
  onboarding Agent step call this after native `gh auth login`; the new admin
  browser flow completes it automatically, without **I've logged in**. See
  [environment-home.md](environment-home.md#connect-github).

## `/local/v1` verbs

`POST /local/v1/<verb>` with one JSON object as the body (an empty body is
an empty params object). Failures answer the same error envelope and
status mapping as the proxied API; an unknown verb answers `404` with
`-32601`. These verbs run with the user's own filesystem and git
authority.

| Verb | Request | Response |
| --- | --- | --- |
| `link.apply` | `{"addr":"host[:port]","invite":"...","name":"..."}` (`invite` and `name` optional) | `{"addr":"host:2222","user":"aether","member":{"id":"...","display_name":"...","role":"..."},"key_generated":"/home/u/.ssh/id_ed25519"}` (`key_generated` omitted when no key was created) |
| `link.status` | `{}` | `{"linked":bool,"server_configured":bool,"addr":"...","user":"...","repo":"...","edge_url":"...","server_id":"...","links":[{"name":"...","addr":"...","repo":"..."}],"active":"..."}` (`edge_url` and `server_id` are present only on an edge link; `links` is present whenever a named profile is saved, `active` only when the gateway runs on one; a profile's `repo` is omitted when it records no clone of its own and inherits the top-level one; `server_configured` reports a configured server even when no repository is linked) |
| `edge.login` | `{"edge":"https://...","label":"..."}` (both optional) | `{"state":"pending","edge":"https://edge.onaether.dev","signin_origin":"https://auth.onaether.dev","user_code":"...","verification_uri":"https://auth.onaether.dev/device"}` |
| `edge.status` | `{}` | `{"edges":[{"edge":"https://...","signin_origin":"https://...","account":{"id":"acct_...","provider":"github","subject":"...","login":"...","email":"...","name":"..."},"device":{"id":"...","label":"...","key":"ssh-ed25519 ..."},"error":"..."}],"login":{"state":"pending"\|"signed_in"\|"failed","edge":"...","signin_origin":"...","user_code":"...","verification_uri":"...","account":{...},"error":"..."}}` (`login` only once `edge.login` ran in this process; an entry's `error` replaces its `signin_origin`, `account` and `device` when its stored sign-in cannot be read) |
| `edge.servers` | `{"edge":"https://..."}` (optional) | `{"edge":"https://...","servers":[{"id":"...","name":"...","online":bool,"role":"admin","access_policy":"account"\|"approved-devices","kind":"self-hosted"\|"hosted"}]}` |
| `edge.hostkey` | `{"server_id":"...","edge":"https://..."}` (`edge` optional) | `{"edge":"https://...","server_id":"...","fingerprint":"SHA256:..."}` |
| `edge.link` | `{"server_id":"...","edge":"https://...","addr":"host[:port]","name":"..."}` (all but `server_id` optional) | `{"server_id":"...","edge":"https://...","addr":"...","user":"aether","member":{...}}`, plus `"remote_error"` when the link is saved but the clone's `aether` remote could not be moved |
| `edge.claim` | `{"code":"<claim code>","edge":"https://...","addr":"host[:port]","name":"..."}` (all but `code` optional) | as `edge.link` |
| `edge.join` | `{"link":"<join link>","addr":"host[:port]","name":"..."}` (`addr` and `name` optional) | as `edge.link` |
| `edge.logout` | `{"edge":"https://..."}` (optional) | `{"edge":"https://..."}` |
| `link.switch` | `{"name":"..."}` (empty selects the default link) | `{"active":"<name>"}` after relinking the gateway in place; the dashboard reloads. It stops the gateway's forwards and syncs first and saves the choice as `gui_server` in `config.json`, which a later `aether gui` without `--server` starts on. Naming the active link changes nothing. An unknown name is `-32602`: `no server named "<name>"` |
| `link.repo` | `{"repo":"/path/to/clone","workspace_id":"..."}` (`workspace_id` optional) | `{"repo":"...","remote":"aether","url":"...","origin":"..."}` (`origin` is the workspace checkout `Origin` afterwards, omitted when it has none) |
| `git.identity` | `{}` | `{"name":"Ada Lovelace","email":"ada@example.com"}` - this machine's `git config user.name` and `user.email`; either is empty when unset |
| `pull` | `{"run_id":"..."}` | `{"branch":"...","ref":"...","output":"...","current":bool,"dirty":bool}` |
| `pull.switch` | `{"run_id":"..."}` | `{"branch":"..."}` |
| `repo.push` | `{"workspace_id":"..."}` (optional) | `{"branch":"...","remote":"aether","state":"pushed"\|"up-to-date"\|"behind"\|"diverged","local_commit":"...","workspace_commit":"...","ahead":0,"behind":0,"output":"..."}` |
| `repo.fast-forward` | `{"workspace_id":"..."}` (optional) | `{"branch":"...","commit":"...","current":bool,"dirty":bool,"output":"..."}` |
| `sync.start` | `{"run_id":"...","force":bool}` | `{"run_id":"...","state":"running"}` |
| `sync.stop` | `{"run_id":"..."}` | `{"run_id":"...","state":"stopped"}` |
| `sync.status` | `{}` | `{"sessions":[{"run_id":"...","state":"...","conflict":"..."\|null}]}` |
| `daemon.install` | `{"server":"host:port","repo":"..."}` (`repo` defaults to the linked one; the unit gets the linked `--key`) | `{"unit_path":"...","note":"..."}` |
| `daemon.status` | `{}` | `{"installed":bool,"unit_path":"..."}` |
| `env.agents` | `{}` | `{"agents":[{"name":"claude","installed":bool},...],"searched":["/usr/local/bin",...],"warning":"...","repo_path":"..."}` - the setup-capable agents in order, with whether each executable is on this machine's `PATH`. The verb first widens the gateway's `PATH` from your login shell (`$SHELL -l -i`, bounded to 5 seconds), so agents installed through a shell profile or since the gateway started are found; `searched` is the resulting `PATH` as a list of folders (always present, may be empty); `warning` is present only when the login shell could not be asked, carrying that error verbatim (the standard folders `/usr/local/bin`, `/opt/homebrew/bin`, `~/.local/bin`, and `~/.bun/bin` were still checked); `repo_path` is the repository folder the saved link config knows, present only when exactly one is known, for prefilling the wizard's from-repo folder input |
| `forward.start` | `{"target":"run:<run-id>|terminal","port":1455}` | `{"target":"run:<run-id>|terminal","port":1455,"local_port":1455,"state":"active"}`; idempotent for the same target and port |
| `forward.stop` | `{"target":"run:<run-id>|terminal","port":1455}` | `{"target":"run:<run-id>|terminal","port":1455,"state":"stopped"}` |
| `forward.status` | `{}` | `{"forwards":[{"target":"run:<run-id>|terminal","port":1455,"local_port":1455,"conns":1}]}` sorted by target, then port |
| `update.check` | `{"refresh":bool}` (optional; `true` skips the cached release lookup) | `{"cli":{...},"cli_error":"...","server_version":"v1.2.9","server_behind":bool,"server_error":"...","client_update_required":bool,"min_client_version":"v1.2.9","supervised":bool,"shell_build_error":"...","cli_path":"/usr/local/bin/aether","install_method":"direct"\|"admin-prompt"\|"manual","target_version":"v1.3.0","server_first":bool,"can_self_update":bool}` (`target_version` empty when there is no release to move to or `server_first` is true; `cli_error` only when the release lookup failed; `server_error` only when the server did not answer; `shell_build_error` only when the last in-app desktop rebuild failed; `cli_path` and `install_method` absent when the binary could not be probed) |
| `update.apply` | `{}` | `{"updated":["/usr/local/bin/aether"],"version":"v1.3.0","restarting":bool,"rebuilding":bool,"note":"...","restart_command":"..."}` (`restart_command` only when `aether-server` was replaced too) |
| `update.status` | `{}` | `{"phase":"packaging","lines_tail":["..."],"error":"..."}` - the desktop-app rebuild `update.apply` started (`error` only when `phase` is `error`) |
| `workspace.selection` | `{}` reads; `{"workspace_id":"..."}` saves; an empty ID clears | `{"workspace_id":"..."}` |

`link.apply` saves the link and swaps the gateway connection in place, so
subsequent API and WebSocket requests use the new server without a restart. If
no SSH key is offered and the server requires one, it may create
`~/.ssh/id_ed25519` and its `.pub` file.

The `edge.*` verbs let the onboarding wizard, and Settings › Remote
access, reach a server through an [edge](edge.md) without a terminal. Each does what the command beside it
does, with the same files in the config directory:

| Verb | Command |
| --- | --- |
| `edge.login` | `aether login` |
| `edge.servers` | `aether servers` |
| `edge.hostkey` | the host key `aether link --from-edge` shows before it asks |
| `edge.link` | `aether link <server id>` |
| `edge.claim` | `aether link --claim <code>` |
| `edge.join` | `aether link <join link>`, one attempt |
| `edge.logout` | `aether logout` |

- `edge` names the edge by its relay origin. Without it a verb uses the one
  edge this machine is signed in to, else `https://edge.onaether.dev`. An
  address that is not `https://host[:port]`, or `http://` on loopback,
  answers `-32602`.
- `edge.login` answers once the edge has registered the sign-in. The person
  opens `verification_uri` and confirms `user_code`; the gateway polls the
  edge in the background and `edge.status` reports `pending`, then
  `signed_in` or `failed` with the client's own error. `label` names the
  device on the edge's Devices page and defaults to the host name. A second
  `edge.login` replaces one still pending. The device code the gateway polls
  with and the device token it receives are never in an answer; the token
  goes only to `edge-tokens.json`, mode `0600`.
- `edge.hostkey` reads the SSH host key the server presents through the
  edge, refuses one that does not derive `server_id`, and ends the
  connection before authenticating. The wizard shows its fingerprint, with
  the server's name and id, before it links a server from the edge's list,
  and asks for a confirmation; a server id typed from the admin links
  without one.
- `edge.link` and `edge.claim` save the link and swap the gateway connection
  in place, as `link.apply` does; under `aether gui --server <name>` a link
  to the same server (same address or server id) is saved into that named
  profile, and a link to another server is added as a new named link, called
  `name`, else the server id or address (suffixed `-2`, `-3` when taken), and
  becomes `gui_server`. No existing link changes. They move the clone's `aether`
  remote from the previous git host to the new one, and report a remote
  that could not be moved as `remote_error` without failing the call. `server_id` must be a server id, and the
  server's SSH host key is checked against it before anything is sent.
  `edge.claim` checks the host key against the id in the code before it
  sends the code, inside SSH. `addr` is an SSH address tried before the
  edge; a claim never uses it.
- `edge.join` takes an invitation's link, `https://<edge host>/join/<server
  id>/<invitation id>#<secret>` or `aether://join?edge=...&server=...&invitation=...#<secret>`;
  the edge comes from the link, and a malformed one answers `-32602`. It
  checks the host key against the server id in the link before it sends
  the secret, inside SSH, then saves the link as `edge.link` does. While the
  joining device waits for an admin's approval it answers `-32002` with
  `data.refusal.kind` `device-pending` and records nothing more; the caller
  calls it again until it succeeds. `edge.claim` takes the claim code; the
  dashboard turns a pasted or handed claim link into one.
- `edge.logout` revokes the device token at the edge and forgets it, and
  drops a pending `edge.login` for that edge.
- Errors carry the client's own message. Not signed in, or an edge refusal
  with a `4xx` status such as a revoked token or a claimed server, answers
  `-32002`. An edge that cannot be reached or answers `5xx` answers
  `-32004`. `edge.link`, `edge.claim` and `edge.join` answer `-32002` for
  any failure of the SSH connection, with the server's refusal text, such
  as a wrong claim code or a device waiting for approval; an invitation that
  names another account is `data.refusal.kind` `join-wrong`. A refusal on the
  edge path, by the server, the edge, or this machine holding no sign-in,
  also carries `data: {"refusal": {"kind": "device-pending", ...}}` with
  the kind and fields of `Refused` in `internal/edge/proto/refused.go`; so
  does the `-32004` `server unreachable: ...` answer of `/api/v1/*` and of
  the `/ws/events` subscribe refusal when the gateway's connection to the
  server fails that way. The dashboard reads `kind`, never the message.

The member's devices and edge invitations are control-channel methods, so
the dashboard calls them through `POST /api/v1/<method>` like any other:

- `member.device.list`; a device awaiting approval also carries
  `display_name` and `role`, whom approving admits it as (no
  `display_name` for a new member).
- `member.device.lookup` (`{"device_id":"..."}` or `{"code":"..."}`).
- `member.device.approve` (`{"device_id":"...","code":"..."}`): approves
  by id; `code` is optional and, when given, must name that device.
- `member.device.revoke` (`{"device_id":"..."}`).
- `member.invitation.create` and `member.identity.link`, which answer
  `{"invitation":{...},"link":"https://<edge host>/join/<server id>/<invitation id>#<secret>","server_id":"..."}`:
  the link is the only copy of the join secret. `member.identity.link`
  links to the caller's own member, for any approved member.
- `member.invitation.list`, `member.invitation.revoke`
  (`{"invitation_id":"..."}`), `member.identity.list`
  (`{"member_id":"..."}`), `member.identity.remove`
  (`{"member_id":"...","provider":"github","subject":"..."}`) and
  `server.owner.transfer` (`{"member_id":"..."}`).
- `member.merge.start` answers `{"code":"XXXX-XXXX-XXXX-XXXX","expires_at":"..."}`,
  shown once. `member.merge.preview` (`{"code":"..."}`), from the other
  sign-in, answers the plan: `minted` and `proposer`, each with `id`,
  `display_name`, `role`, `tailnet_login`, `public_key_fingerprint`,
  `identities` (`provider`, `login`), `devices` (`id`, `label`, `status`),
  `runs`, `schedules` and `settings`; the merged `role`, with
  `role_with_keep_admin` and `keep_admin_required` when one side is an
  admin; `sign_ins`; `choices`, one per `keep_settings_of` with the
  `discarded` settings; and `plan_digest`. It carries no email address,
  device key or agent definition. `member.merge.propose`
  (`{"code":"...","keep_settings_of":"<member id>","plan_digest":"...","discard_ack":true,"keep_admin":bool}`)
  answers the proposal: `id`, `minted_id`, `proposer_id`,
  `keep_settings_of`, `plan_digest`, `expires_at` and `plan`.
  `member.merge.pending` answers `{"proposals":[...]}`, the open ones the
  caller made or is asked to accept, each with its plan recomputed and a
  `refusal` when accepting it would now be refused: a minter's Needs you
  item. `member.merge.accept`
  (`{"proposal_id":"...","plan_digest":"...","keep_admin":bool}`), from the
  minter, answers `{"merge":{...},"member":{...}}` and then closes the
  absorbed member's connections, the caller's included once it has the
  answer; a new connection is the survivor. `member.merge.cancel`
  (`{"proposal_id":"..."}`, or `{}` for all) answers `{"cancelled":n}`.
  `member.list` also carries `merged_into`, which maps each absorbed
  member's id, still named in history, to the member it is now.
  `member.merge.list` answers `{"merges":[...]}`, each with `archive_path`:
  the caller's as survivor, every one for an admin. A refused step answers
  `-32002` with the reason, such as `the members changed since the
  preview; preview again`, and a connection signed in with a device no
  person approved `-32001`.
- `server.edge`, for any member:
  `{"edge_url":"https://...","server_id":"...","access_policy":"approved-devices","owner":{"provider":"github","login":"..."},"connected":bool,"claimed":bool}`;
  `edge_url` is empty with the edge off, `owner` is `null` without one, and
  `claimed` is whether the edge last reported the server claimed.
- `server.edge.claimcode`, for an admin on an approved device, member SSH
  key or tailnet connection: `{"code":"...","expires_at":"..."}`, a claim
  code bound to the caller's own member, for the dashboard to pass straight
  to `edge.claim`.

A device recorded pending, approved or revoked is published as the
`member.device` event, `{"device_id":"...","status":"..."}`, and a merge
as `member.merged`, `{"merge_id":"...","survivor_id":"...","absorbed_id":"..."}`,
under the workspace id `server`, which names no workspace, so a server without
workspaces still reports it. The shapes are in
`internal/protocol/identity.go` and `internal/protocol/edge.go`, and
[edge.md](edge.md) describes what each does.

`workspace.selection` stores only the last selected workspace ID in
`workspace-selection/<hash>.json` beside the CLI's `config.json`. The hash
keys the linked server address and authenticated member ID from `server.info`;
different members and servers keep separate selections. Writes replace the
file atomically with mode `0600`. The gateway token and same-origin checks
apply. The dashboard reads the preference at startup and saves changes in
order, so an ephemeral gateway port does not reset the selection. A deleted
selection falls back to a remaining workspace, or clears when none remain.
An unreadable preference produces a dashboard error toast but does not prevent
the server snapshot from loading.

- `link.repo` honors a `workspace_id` naming the workspace the remote URL
  must carry. Onboarding and workspace repository settings always send the
  selected ID, as do `repo.push` and `repo.fast-forward`; they never rely on
  single-workspace inference. Without it, the API still resolves like
  `aether link --repo`: a single workspace resolves implicitly; none or several
  answers `-32002` (invalid state). Use `--workspace <name-or-id>` from the CLI.
- The gateway keeps one current repository per server profile. Linking another
  workspace replaces that current clone, not a hidden per-workspace mapping.
  The UI checks `link.status` before mutations; the gateway verifies that the
  current clone's `aether` remote targets the supplied workspace.
  Linking under `aether gui --server <name>` saves the repository in that named
  profile without overwriting defaults or siblings. If its saved server
  identity changed or disappeared, restart the GUI before linking.
  Creation and linking responses belong to the initiating form, connection and
  authenticated identity. Closing that form or changing context invalidates
  late UI updates without replaying the mutation. After linking, the dashboard
  reads authoritative `link.status` before showing the connected clone.
- `workspace.mirror.status` requires read access; admitted collaborators and
  viewers can inspect the existing public result without changing a mirror.
  `configure`, `refresh`, `adopt`, and `disable` remain admin-only.
  `configure` receives the verified source URL, branch, auth mode, and (for
  generic SSH) the `known_hosts` file contents; the server generates and stores
  any deploy key.
  Results expose source, branch, status, observed/accepted commits, check
  times, and the public key only. The source mirror dialog on the dashboard's
  repository page calls these same methods.
  If refresh fails before observing a commit, `status` retains the last observed
  and accepted commits alongside the new status and `last_error`. Sources with no
  observation keep empty commit fields. Retention does not make the source
  fresh or permit an ordinary launch after failure. Reconfiguration clears
  the commit fields; disabling removes mirror metadata.
- A mirrored workspace's base branch is server-owned. `repo.push` and
  `repo.fast-forward` retain their normal local-only behavior, but a direct
  base write against a mirrored policy is rejected; refresh or explicitly
  adopt the mirror candidate instead.
- `repo.push` seeds the workspace. It first asks the `aether` remote for
  its copy of `<base>` with `git ls-remote --heads`, where `<base>` is that
  workspace's base branch. Only when the remote answers a tip does it fetch
  that branch into `refs/remotes/aether/<base>` and compare the two; on a
  fresh workspace no fetch runs at all. `state` reports one of four
  findings:
  - `pushed` - the workspace had no such branch, or the local branch was
    ahead. The push ran.
  - `up-to-date` - both tips are the same commit. Nothing was pushed.
  - `behind` - the workspace is ahead of the clone. Nothing was pushed;
    `repo.fast-forward` catches the clone up.
  - `diverged` - both moved on. Nothing was pushed, nothing forced.
- Another member can advance the workspace branch in the window between the
  compare and the push, and git then rejects the push as a non-fast-forward.
  `repo.push` compares once more and answers `behind` or `diverged` from
  that second compare; `output` carries git's rejection and the second
  compare's fetch. When the second compare does not explain the rejection -
  it answers `missing`, `same`, or `ahead` - the push failure stands as
  `-32603`, because nothing landed and no `state` may suggest otherwise.
- `local_commit` and `workspace_commit` are the two branch tips as full
  40-hex commit ids, `workspace_commit` the empty string when the workspace
  has no such branch yet. `ahead` and `behind` count the local branch's
  commits relative to the workspace branch, measured before any push. Both
  are 0 unless the workspace already carried the branch and the two tips
  differ, so `pushed` on a fresh workspace and `up-to-date` always report 0
  and 0. `output` is everything git printed: the fetch when one ran, plus
  the push when one ran.
- The push itself is one
  `git push --no-follow-tags -u aether refs/heads/<base>:refs/heads/<base>`
  in the linked repository. The same sole-workspace rule as `link.repo`
  applies when `workspace_id` is omitted; unlike `link.repo`, a
  `workspace_id` no workspace carries is refused. The refspec is fully
  qualified so the push carries that one branch and nothing else: no force,
  no second ref, and no tags even where `push.followTags` is set.
- `repo.push` refuses with `-32002` (invalid state), naming the next step,
  when the repository has no commits, has no local branch named `<base>`
  (the message names the branch that is checked out instead), has no
  `aether` remote yet, or has an `aether` remote pointing at a different
  workspace than the one asked for - the branch would come from one
  workspace and the objects would land in another. A failed `ls-remote`,
  fetch or push answers `-32603` carrying git's own output: an unreachable
  server or a key git could not use fails the compare before any push, and
  a push the server rejected for branch protection fails after it. The one
  push failure that is not an error is the non-fast-forward rejection above.
- `repo.fast-forward` resolves the `behind` state: it compares exactly as
  `repo.push` does, then advances the local branch to the workspace's tip.
  Fast-forward only - it writes no merge commit and never rewrites commits
  already on the local branch. `commit` is the local branch tip afterwards,
  as a full 40-hex commit id.
- The branch keeps whatever tracking remote it already had. A member's
  `<base>` usually tracks their own remote, and a catch-up that repointed it at
  the workspace would redirect their next `git pull`; only run branches
  `pull` creates track `aether`.
- Another branch checked out does not stop it: exactly as `pull` does, it
  updates the branch ref and leaves the working tree alone. When `<base>` is
  the checked-out branch, git fast-forwards it in place and refuses when
  that would overwrite an uncommitted change, in git's own words
  (`Your local changes to the following files would be overwritten by
  merge`). `current` reports whether the checkout is on that branch and
  `dirty` reports uncommitted changes afterwards.
- A `<base>` checked out in one of the repository's linked worktrees is
  refused instead, with `-32002`: git will not move a branch from outside
  the worktree holding it, and no other worktree is ever written to. The
  message names that worktree and the
  `git -C <worktree> merge --ff-only aether/<base>` that catches it up
  there. `pull` refuses a run branch held by another worktree the same way.
- `repo.fast-forward` refuses with `-32002` (invalid state) on the same
  local preconditions as `repo.push`, and in every state but `behind`,
  naming what to do instead: the workspace has no branch named `<base>` yet
  and it is pushed instead; `<base>` already matches the workspace and there
  is nothing to fast-forward; `<base>` is ahead of the workspace and it is
  pushed instead; or both sides moved on, so no fast-forward is possible and
  the member rebases onto `aether/<base>` or merges it, then pushes.
  A local branch move git itself refused, most often that uncommitted
  change, answers `-32002` too, carrying git's message, because the member
  fixes it in their own repository. Only a failed fetch - an unreachable
  server, a key git could not use - answers `-32603`.
- Every git command that dials a remote is bounded at ten minutes and runs
  with `GIT_TERMINAL_PROMPT=0`, so git cannot block on its own credential
  prompt. The bound covers the compare and the push separately, so a
  `repo.push` that compares and then pushes can take twenty minutes;
  `repo.fast-forward` bounds its compare at ten and finishes locally. The
  bound does not reach `ssh`: a passphrase-protected key with no agent still
  waits on ssh's own prompt until the ten minutes are up. Load the key into an
  agent before pushing or fast-forwarding from the dashboard.
- `pull` fetches the run branch, fast-forwards it when it is checked out, and
  otherwise creates or updates the local branch without switching branches.
  `current` reports whether the checkout is on that branch and `dirty` reports
  uncommitted changes after the operation. `pull.switch` refuses a dirty
  checkout and switches to the pulled branch when it is clean.
- `pull` answers `-32002` (invalid state) for a branch move the member
  fixes in their own repository, the run branch held by another worktree
  above being the one such state, and `-32603` for anything else git ran
  and lost. Both carry git's own words.
- A gateway started with `AETHER_PACKAGED` set, as the Android app does with
  `Android app`, is part of an app that installs its own updates and has no git
  checkout, service manager or local agents. It leaves out `daemon.install`,
  `daemon.status`, `env.agents`, `git.identity`, `link.repo`, `pull`,
  `pull.switch`, `repo.fast-forward`, `repo.push`, `sync.start`,
  `sync.status` and `sync.stop` from `local`, and `forward.start`,
  `forward.status` and `forward.stop` too: a forward listens on `127.0.0.1`,
  which every app on a phone can reach. `update.check` still reports the
  newest release, with `packaged` set, `can_self_update` false and no
  `asset`, so the dashboard can say an update exists, and `aether update
  --check --json` prints the same check for the app's own banner.
  `update.apply` refuses from the configuration, before any lookup, and
  `aether update` without `--check` refuses likewise, both with `this aether
  is part of the Android app, which installs its updates; update the Android
  app instead`: the APK is the update unit, and the sidecar never replaces
  its own binary.
- On Android the CLI reads `AETHER_RESOLV_CONF`, a resolv.conf path, only when
  `/etc/resolv.conf` is absent; the app writes the phone's DNS servers there
  ([networking.md](networking.md#android-dns)).
- `pull`, `pull.switch`, `repo.push`, `repo.fast-forward`, `sync.start`, and
  `sync.stop` refuse with `-32002` when no repo is linked.
- A sync session's states are `starting` (the overlay is dialing the run
  worktree), `running`, `stopped`, `conflict` (with the conflict text in
  `conflict`), and `error`. A conflict is also reported to the server as a
  `sync.conflict` call so both affected members see the event;
  `sync.stop` dismisses a standing conflict.
- `update.check` answers the release check `aether update --check --json`
  prints, under `cli`, beside the linked server's version. Its fields are
  `version`, `commit`, `latest`, `update_available`, `asset`, `release_url`,
  `dev`, `disabled`, `can_self_update`, `packaged` and `checked_at`
  ([install.md](install.md#upgrading)). The gateway resolves the latest
  release at most once an hour and serves the cached answer in between, so
  a page load never costs a request to GitHub. `refresh: true` skips the
  cache and dials, and the answer it resolves replaces the cached one; the
  dashboard sets it on the read behind the Update button, which names the
  release about to be installed, and on every **Check for updates…**. A refresh that cannot reach GitHub reports
  `cli_error`, `check for releases: <the lookup's own message>`, rather
  than the cached answer, and it leaves any cached success in place. The
  verb still answers 200, with `cli` holding only this build's own fields,
  so the server half shows during a GitHub outage.
  `client_update_required` is true when the server's `min_client_version`
  is newer than this CLI, which means the server refuses it with `-32005`.
  It is false when the server does not answer, or when either version is
  not a release. A failed lookup is cached for five minutes, so an
  offline machine is not re-dialed every time a page loads or a window
  comes back. Lookups are numbered, so one that started earlier never
  replaces the cached answer of one that started later: two straddling a
  release would otherwise leave the superseded tag cached for the hour.
  Those two are the verb's only errors, plus `-32602` for a `refresh` that
  is not a boolean. `server_behind` compares the linked server's version
  with that same latest release; `supervised` reports
  whether this gateway was started by the desktop shell
  (`aether gui --json`), which is what decides whether `update.apply` may
  restart it. `shell_build_error`
  is present only when the last in-app desktop rebuild failed, and carries
  that build's own error. `target_version` is the release this CLI should
  move to, so the dashboard can name it before the click: the linked
  server's release (`server_version` without a `git describe` tail) when it
  is a release tag newer than this CLI, otherwise `latest` when that is
  newer, otherwise empty. It is empty too for a dev build and with
  `AETHER_NO_UPDATE_CHECK` set. `can_self_update` is true when
  `update.apply` can install it here; it is false for a dev build and with `AETHER_PACKAGED` set, where `target_version` is still
  reported so the client can link its release page. `server_first` is true
  when the linked server runs a release older than the target, this member
  is an admin, and `server.update_status` reports the server `capable` of
  updating itself: `target_version` is then empty and `update.apply`
  refuses with `-32002` and `update the server first: aether server
  update`, the rule `aether update` applies.
- `cli_path` is the binary `update.apply` would replace, symlinks resolved,
  and `install_method` how: `direct` when its directory is writable by this
  user, so the swap just happens; `admin-prompt` when it is not and the
  administrator dialog can install there, so the click opens it; `manual`
  when the gateway cannot replace it from here, and the banner shows the
  command instead. `admin-prompt` needs all four of: the directory is not
  writable by this user; the directory and every directory above it up to
  `/` is owned by root, is not a symlink, has no group or other write bit,
  and carries no access control list (`ls -ld` shows one as a trailing `+`
  in the mode column); this is macOS; and there is a GUI session
  (`/bin/launchctl managername` answers `Aqua`). Anything else is `manual`:
  Linux, Windows, a gateway started over SSH, or a directory that is not
  root's alone, such as one user's Homebrew `/usr/local/bin` (Intel) used
  from another account, or a root-owned bin directory under a user's home
  (`sudo` installed into `~/.local/bin`). The root-only rule is what the
  privileged command relies on: root stages a temp file in that directory
  by name and renames it over the binary by path, so any account that can
  write the directory, or swap a directory above it for a symlink, could
  redirect root's copy, `chmod` and rename between its steps. `sudo aether
  update` has no such gap: it stages with `O_EXCL` and renames its own
  inode, which a fixed shell command cannot. The gateway probes on every
  call by creating and removing one temp file in that directory,
  then the ownership and session checks, the same test `update.apply`
  runs, so the promise and the behavior cannot drift and a reinstall or a
  `chown` shows on the next check; only the release lookup is cached. Both
  fields are absent when the probe itself fails; the click then reports
  that error.
- A `server.info` call that fails costs the server half only: the answer
  still carries `cli`, with an empty `server_version`, `server_behind`
  false, and the backend's own message in `server_error`. The CLI half is
  about a binary on this machine and has nothing to do with the SSH hop, so
  a server outage must not take the CLI update prompt down with it.
- `update.apply` takes the one-install-at-a-time slot before anything else,
  so a second click pays for no lookup, then resolves the latest release
  fresh - never from the cached answer, so a click installs what is newest
  at that moment rather than what was newest when the banner was drawn -
  and asks the linked server for its version. It installs `target_version`
  as `update.check` defines it: a server ahead of this CLI decides the
  release, so the client matches its server, as `aether update --version
  <server release>` would, even when GitHub's latest is older (it skips
  prereleases and can lag a publish). A failed lookup is the call's error
  unless the server named the target; no cached tag stands in for it. It
  then runs the swap `aether update` runs, on the `aether` binary this gateway
  is served from - and `aether-server` beside it on a Linux server host, in
  which case `restart_command` carries the `sudo systemctl restart
  aether-server` the command prints, because the running server keeps the
  old code until its unit restarts. On a supervised gateway it answers
  `restarting: true`; started from a terminal it answers
  `restarting: false` and a note telling the user to rerun `aether gui`. It
  never updates a *remote* server: the dashboard has no authority there,
  and the server banner names the commands to run on that host instead. A
  second `update.apply` for a release this gateway process already
  installed does not download or prompt again: the binary is already on
  disk, so the answer picks up after the swap. Both that guard and the
  desktop-app rebuild key on the tag this call resolved, so a newer release
  that shipped in between is installed and rebuilt rather than skipped.
  When this release's rebuild already finished in this process, no second
  rebuild starts: it answers `rebuilding: false` with the note `the desktop
  app was rebuilt; restart it to use the new version`, and a supervised
  gateway does not exit again. One build runs at a time, so a newer release
  arriving while an older one is still building has no build of its own
  yet: that answers `rebuilding: false` with the note `a rebuild of an
  earlier release is still running; rerun the update once it finishes`.
- This process never gains privileges. Where the binary's directory is
  writable (`install_method: "direct"`) the release is downloaded, verified
  against `checksums.txt`, staged beside the binary and renamed over it,
  exactly as the command does. On macOS with a directory this account
  cannot write (`admin-prompt`) the route is longer, and its one privileged
  step runs outside this process:

  1. It downloads and verifies the release as the user into a private
     staging directory, `<user cache>/aether/update`
     (`~/Library/Caches/aether/update`), created `0700` and refused when
     something else is there - a symlink, another user's directory -
     because root will read from it.
  2. It runs `/usr/bin/osascript -e 'do shell script "<command>" with
     prompt "<text>" with administrator privileges'`, which shows macOS's
     standard administrator dialog: titled `osascript`, Aether's text
     beneath it (`Aether wants to replace /usr/local/bin/aether with aether
     v1.3.0. macOS shows this request as osascript, the tool Aether asks
     through. Aether never sees your password.`), and the system's own
     last line, "Touch ID or enter your password to allow this." The
     password or Touch ID match goes to macOS's authorization service;
     nothing is stored, piped, or logged by Aether. Root runs one fixed
     `sh` command made of
     system tools - a `0600` temp file in the destination directory, a
     copy, a SHA-256 check against the digest baked into the command text,
     `chmod 0755`, `mv -f` - with the environment
     `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, `LANG=C`, `HOME`, and `/` as its
     working directory. The command is quoted verbatim in
     [security.md](security.md#client-self-update-on-macos).
  3. It re-checks the installed file as the user - a regular file, mode
     `0755`, root-owned, hashing to the release digest - before rebuilding
     the desktop app or exiting.

  The digest is checked three times: on the download by the user, on the
  root-owned copy by root, on the installed file by the user. Linux and
  Windows are unchanged in kind; the only Linux-visible change is that the
  banner shows `sudo aether update` before the click instead of a button
  that fails.
- The call waits on the request context only: there is no dialog timeout,
  because a native authorization dialog has none, and a request that closed
  under the user would leave the dialog on screen with the password
  authorizing nothing. Closing the tab or the app cancels the request; a
  dialog already on screen may stay until dismissed, and the password then
  authorizes nothing. One install runs at a time per gateway.
- The binary swap is synchronous; the desktop-app rebuild that follows it
  is not. When an app is installed for this account, `update.apply` spawns
  `<the new aether> gui build --json` in the background, answers
  `rebuilding: true`, and the dashboard polls `update.status` for progress.
  The new binary runs the build because the Electron shell sources ship
  inside it - the process answering this call is the one being replaced. A
  machine with no app installed answers `rebuilding: false` and builds
  nothing. One build runs at a time: a second `update.apply` while one is
  still going answers `rebuilding: true` with the note `a rebuild of the
  desktop app is already running`, and starts nothing - and, critically,
  does not exit a supervised gateway, which would drop the shell back into
  the old app mid-swap. The build child belongs to the gateway: closing the
  gateway kills it rather than leaving it downloading Node and swapping the
  app directory on its own.
- `update.status` reports that rebuild: `phase` is `idle` before any
  rebuild has run in this process, then the `gui build --json` phases
  (`downloading` for the prebuilt Windows app; `unpacking`, `fetching
  node`, `installing dependencies`, `packaging` for a build from source;
  then `installing`), then `done` or `error`. `lines_tail` carries the last 20
  lines of the build's own output, and `error` the build's own message. A
  gateway that comes up after a rebuild answers `idle`: the build belonged
  to the process that exited.
- A supervised gateway exits only once the rebuild ends: **75** on success,
  so the shell relaunches onto the new app, and **0** on failure, so the
  shell respawns the sidecar on the new CLI. On Windows the rebuild only
  *stages* the app (`aether gui build --json --stage` downloads or builds it
  into `<user cache>/aether/desktop-build/staged-app` and installs nothing),
  because the app directory cannot be renamed while the shell runs and the
  gateway dies with the shell. The gateway then starts the updated CLI as a
  detached helper, `aether gui build --staged --after-exit <shell pid>,<gateway pid>`
  (`DETACHED_PROCESS`, no `powershell.exe` or `cmd.exe`), and exits **76**.
  The helper waits for both processes by handle, for at most a minute; ends
  the `aether-daemon` Scheduled Task if it exists (it starts again at the next
  sign-in, or with `schtasks /Run /TN aether-daemon`); swaps the app in with
  the same rename-and-roll-back install as `aether gui build`; and starts the
  app. If a step fails, the helper leaves the old app in place, starts it,
  and writes the error to `last-error.txt`, so the next dashboard shows it as
  `shell_build_error`. If the helper itself cannot start, the gateway
  records that and exits **0**, leaving the old app running. A failed build is also written
  to `<user cache>/aether/desktop-build/last-error.txt`, which the next
  gateway's `update.check` returns as `shell_build_error` so the dashboard
  can show what went wrong; the next successful `aether gui build` removes
  it. An *unsupervised* gateway never exits: it rebuilds the app and the
  note tells the user to restart it.
- `update.apply` errors, each carrying the underlying message verbatim:
  - `-32001` (denied, 403): the user cancelled the dialog, or macOS refused
    the password and gave up. `nothing was changed: administrator access was
    not granted: ... execution error: User canceled. (-128)` - the tail is
    osascript's own line, where `...` is its position prefix, which varies;
    the gateway reads only the trailing number. The banner shows it muted
    as *Update cancelled, nothing was changed.* and the button comes back.
  - `-32002` (invalid state, 409): a directory this account cannot write
    and no dialog to install through - Linux, or a macOS gateway with no
    GUI session (started over SSH), which `update.check` already reported
    as `manual`: the same refusal the command prints, ending in ``re-run as
    `sudo aether update` ``, before anything is downloaded. If osascript
    still reports no session at run time, ``no GUI session to show the
    macOS authorization dialog in: ... (-1713); run `sudo aether update` in
    a terminal on this Mac``. Also a dev build, `AETHER_NO_UPDATE_CHECK`
    set, Windows, and nothing to install: `already on <latest>, the newest
    release` when neither the server nor the latest release is ahead of
    this build (downloading it over itself would report success for work
    that changed nothing, and restart the app for nothing), or `no signed
    release is published yet; this aether stays on <version>` when no
    release has a signed manifest and the server names none.
  - `-32003` (conflict, 409): `an update is already running in this
    gateway` - a second click from another tab while the dialog is up.
  - `-32004` (unavailable, 503): everything else, carrying the real
    message after an `install <tag>: ` prefix - a download or checksum
    error, osascript failing to start, root's checksum check failing
    (`install v1.3.0: replace /usr/local/bin/aether: osascript: ...
    execution error: copied binary does not match the release checksum
    (65)`, with the same `...` position prefix as above), or
    the post-install check failing (`install v1.3.0: installed
    /usr/local/bin/aether does not match the release checksum; do not run
    it`). A release lookup that fails answers the same code with the
    transport's own error after `check for releases: `.

## WebSockets

Cross-origin WebSocket handshakes are rejected; the SPA is served from the
same origin as the API. On `aether gui`, each handshake carries the
per-process token as `Authorization: Bearer` or `?token=` - browsers cannot set
headers on a WebSocket handshake. A missing or stale local token is refused
with `401` before the upgrade, so it never becomes a socket; the dashboard's
capabilities probe catches that case ahead of the stream. The server gateway
carries no token: WhoIs identifies the request's source address instead.

Both transports reconnect on a jittered backoff that caps at 30 seconds, and
reopen immediately - backoff reset - when the browser fires
`visibilitychange` (visible) or `online`. A phone freezes a background tab's
timers, so without those two events a tab returning from the pocket would sit
out the rest of a 30-second wait. A foreground return leaves a socket that is
still there alone; `online` replaces it whatever state it reached, because a
network switch leaves even an acknowledged socket half open, with the browser
still reporting it as connected and no close ever arriving on the client side.
If an attach is still inside its bootstrap boundary when `online` fires, the
client cancels that parser and drain with an explicit cancellation signal,
clears the partial operations, and drops the socket while keeping the terminal
hidden. The replacement attach starts a fresh hidden bootstrap; the incomplete
prefix is never revealed. For a dashboard run attach the replacement is a
compact current-screen snapshot captured without scanning the retained raw
archive. If the replacement is finally refused, the client settles the
bootstrap gate before showing the server's error. An attach the gateway
refused, one parked on a `session ended` close, and a run still waiting for
its PTY session are not reopened by either event.

Every live socket - `events`, `attach`, `acp`, and `terminal` - is pinged by the
server every **30 seconds** and closed when the pong does not arrive within
**10**. A client that changed networks or went to sleep leaves a half-open
connection that reads as live on both ends; the ping is what releases the PTY
client it was holding, whose geometry may be clamping every other viewer. The SPA
reconnects on its normal path.

### `GET /ws/events`

Same subscription semantics as the shared event-stream subsystem, so a client
that lost its socket resumes without gaps.

1. Client sends one **text** frame: a `SubscribeRequest`. The header must
   arrive within 10 seconds or the socket is closed; frames sent after it
   are discarded, so an application-level keepalive does not tear the
   stream down.

   ```json
   {"workspace_id":"","run_id":"","types":[],"replay":true,"after_seq":412}
   ```

2. Server answers one **text** frame: `{"ok":true}`, or
   `{"ok":false,"code":-32004,"error":"..."}` followed by a close.
3. Server then streams one **text** frame per event, each a `protocol.Event`:

   ```json
   {"id":"evt_01H...","seq":413,"time":"2026-08-14T10:00:00.123456Z",
    "workspace_id":"ws_...","run_id":"run_...","actor_id":"mem_...",
    "type":"run.status","payload":{...}}
   ```

Reconnect contract: track the highest `seq` you have seen and resubscribe
with `"replay":true,"after_seq":<last seq>`. When the event stream ends for any
reason - a dropped connection, a server restart, or a per-client buffer
overflow - the socket closes with code **1012** (service restart), reason
`event stream ended; resubscribe with after_seq`. That close is the signal to
resubscribe from your last `seq`, not an error to surface; the replay recovers
anything dropped.

### Assistant notices

When a member's [assistant](assistant.md#notifications) launches or messages a
run, the server publishes `assistant.notice` as the run finishes or needs the
member. The envelope carries the run's `workspace_id` and `run_id`, and the
member as `actor_id`. It is a workspace event, so every subscriber of the
workspace receives it; a client shows it only to the member in `member_id`.

```json
{"type":"assistant.notice","payload":{"member_id":"mem_...","run_id":"run_...",
 "kind":"needs_input","title":"Run \"Fix flaky login test\" needs your input",
 "body":"Question: Which database should the test use?"}}
```

`kind` is `finished`, `needs_input`, `needs_approval` or `needs_attention`.
The event is published once per announced state.

### `GET /ws/attach/<run_id>`

PTY attach. Output is binary; JSON text frames carry the header, input,
resize, control, geometry, and acknowledgements.

1. Client sends one **text** header frame (the run comes from the path). The
   header must arrive within 10 seconds or the socket closes. A dashboard run
   terminal sends the interactive header:

   ```json
   {
     "write":true,"screen":true,"interactive":true,
     "cols":120,"rows":40,"control_session_id":"tab-7"
   }
   ```
   `screen:true` requests a compact current-screen bootstrap: viewport,
   cursor, terminal modes, colours, alternate buffer, and at most 200
   scrollback rows, not the raw transcript. A live session serializes this
   state in memory at the same output boundary returned by the ack; opening the
   terminal does not scan durable history. Screen dimensions and the server's
   snapshot store are capped at 4,096 rows, 4,096 columns, and 1,048,576 cells.
   The dashboard's xterm independently requests up to 5,000 live-scrollback
   rows and reduces that count as needed to keep its normal and alternate
   buffers within 1,000,000 cells. A finished run whose compact checkpoint is
   temporarily unavailable falls back to at most 1 MiB of recent output while
   checkpoint repair proceeds separately. It never falls through to a complete
   archive replay. `screen:true` is the dashboard run default.
   `screen:false` deliberately selects the retained raw transcript stream for
   compatibility clients such as the CLI; the dashboard does not request it.
   `interactive:true` opts into same-stream acknowledged control frames and is
   the dashboard run default. Shell and CLI attachments do not gain this
   browser control protocol merely by using the attach endpoint.

   An already-mounted dashboard surface deliberately reopening while its
   parsed screen remains valid may send its settled `cursor` and the nonempty
   `resume_id` from the same PTY incarnation:

   ```json
   {
     "write":true,"screen":true,"interactive":true,
     "resume":true,"cursor":4120,"resume_id":"pty-incarnation-7",
     "cols":120,"rows":40,"control_session_id":"tab-7",
     "control_generation":8
   }
   ```

   A valid same-incarnation resume sends only bytes still present in the
   bounded in-memory replay ring and keeps the parsed screen; it never reads
   the transcript to reconstruct a gap. If the cursor, ring, geometry, or
   incarnation cannot serve that gap, the ack says `"resumed":false` and the
   server sends a compact current-screen bootstrap instead. The dashboard
   replaces that hidden surface; it does not replay the retained raw archive.
   A finished run with `screen:true` likewise supplies bounded current/recent
   state read-only. Non-dashboard compatibility consumers may request the
   complete raw archive through `GET` or the legacy form `POST` at
   `/api/runs/<run_id>/terminal-history` instead.

   `follow` remains available for a viewer that must render the session at its
   acknowledged size without imposing local geometry. `cols` and `rows` do
   not override a live or recorded screen's geometry.

   Geometry `follow` is independent of viewport follow-bottom state. Live
   xterm follows output at the bottom; upward reading in the dashboard run
   pane switches to a static, virtualized surface with its own row/pixel
   anchor. Compact bootstrap and live geometry changes do not overwrite that
   saved presentation. For xterm's own structural replay or column reflow,
   viewport restoration remains conditional on a current operation, no newer
   user scroll and the same normal/alternate buffer. On a phone the live pane
   pans horizontally across the acknowledged grid, while the history surface
   owns scrolling during reading; neither adds a competing vertical scroller.
   Ordinary alternate-screen gestures remain application input; `Shift+PageUp`
   is the dashboard's explicit archive gesture, not a protocol operation.

2. Server answers one **text** ack:

   ```json
   {
     "ok":true,"framed":true,"cols":120,"rows":40,
     "replay":4096,"cursor":4120,"resume_id":"pty-incarnation-7",
     "resumed":false,"has_control":true,"control_generation":8
   }
   ```

   A refusal is `{"ok":false,"code":-32001,"error":"..."}` followed by a
   close. `replay` is the exact number of binary bootstrap bytes before live
   output; with `screen:true` those bytes are the bounded compact snapshot,
   while `screen:false` uses the retained raw replay. A binary frame may straddle
   that boundary. The ack's geometry is the captured screen's size, not an
   echo of the header. Successful live attaches return a nonempty
   `resume_id`; finished transcript-only snapshots have no live incarnation.
   The dashboard keeps the surface hidden while it parses framed records and
   serializes xterm writes. User input and terminal-generated replies stay
   muted through the final replay write callback. That callback opens input;
   the surface remains hidden for two paint turns and until structural viewport
   restoration settles, and is revealed only afterward.

   The backend's ordered terminal stream uses binary records on the SSH
   subsystem: an `o` byte and four-byte big-endian payload length precede each
   output record, a `g` byte plus two four-byte dimensions carry geometry, and
   a `c` byte plus a four-byte length carries one JSON control record. At the
   webgate boundary those records are decoded and stripped: output payloads are
   sent to the browser as binary WebSocket messages, while geometry and control
   records become JSON text messages. The server-hosted gateway performs the same
   translation without the SSH hop, so the browser never receives `o`/`g`/`c`
   record headers.
   Replay counts exclude the backend record headers. The browser never allocates
   a transcript-sized buffer from `replay`; it processes frame-sized records in
   wire order and follows the visibility, input, and viewport ordering above.
   Client frames are capped at 64 KiB; the SPA splits larger input in ordered frames.

   A write attach requires **steer** permission and otherwise refuses with
   `-32001`; an unknown run is `-32000`. A `queued`, `provisioning`, or
   `running` run without a session is `-32004` rather than a held socket.
   A finished run remains readable through its snapshot or raw replay,
   depending on `screen`.

3. Server then streams terminal output as **binary** frames. For framed
   dashboard output, live bytes begin after the ack-declared bootstrap
   boundary. Raw clients consume the same boundary without dashboard parsing.

4. Client sends **text** frames:

   ```json
   {"type":"input","data":"ls -la\r","control_generation":8}
   {"type":"resize","cols":132,"rows":50}
   {"type":"control","request_id":17,"write":true}
   {"type":"control","request_id":18,"write":false,"control_generation":8}
   {"type":"takeover","request_id":19,"action":"start",
    "takeover_id":"3c108774-c2e6-40e0-af51-2825ca226135"}
   {"type":"takeover","request_id":20,"action":"confirm",
    "takeover_id":"3c108774-c2e6-40e0-af51-2825ca226135"}
   ```

   `control` changes the lease on this same WebSocket; it does not reconnect
   or replay. An occupied primary terminal uses the timed `takeover` exchange
   below, not an immediate forced acquisition. Interactive `takeover:true`
   is accepted only for reconnect replacement of the exact authenticated
   member, session, and nonzero `control_generation`; it cannot displace
   another session. Raw legacy attaches keep their existing explicit takeover
   behavior. Include the current `control_generation` for release and input.
   Read-only input is ignored and stale input never reaches the PTY.

   Takeover actions share the attach's increasing `request_id` sequence with
   ordinary control requests. `start` supplies a fresh UUID `takeover_id` and
   snapshots the occupied holder. The server refuses unoccupied or already-owned
   targets, concurrent requests, and requesters without current Steer authority.
   Disconnected holders and interactive holders still awaiting their control
   acknowledgement are also refused.
   After a continuous five-second hold, the exact requesting attachment sends
   `confirm` with the same ID. Early or foreign confirms are refused. Nothing
   is granted without confirmation, even if the hold deadline has passed.
   Early release sends `cancel`; release after confirmation does not. Escape,
   blur, or leaving the terminal may explicitly cancel either phase.

   Confirmation starts a seven-second server-timed review. The targeted holder
   sends `accept` or `deny` with the same takeover ID and its exact
   `control_generation`. Only that authenticated attachment can decide;
   knowing another session's ID is insufficient. Acceptance or expiry grants
   only after the server atomically rechecks the captured holder generation
   and live connection, current requester authority, terminal readiness, and
   swarm admission. A raw CLI holder cannot answer the dashboard dialog;
   the same deadline still applies. Either participant disconnecting, holder
   replacement/release, or lost run/member authority cancels the request.
   Reconnecting the same generation does not revive an old request.

   Both interactive participants receive prompt `type:"takeover"` snapshots:

   ```json
   {
     "type":"takeover","request_id":20,"ok":true,
     "takeover":{
       "id":"3c108774-c2e6-40e0-af51-2825ca226135",
       "requester_member_id":"member-2","requester_session_id":"tab-8",
       "holder_session_id":"tab-7","holder_generation":8,
       "phase":"review","hold_started_at":"2026-10-03T12:00:00Z",
       "hold_deadline":"2026-10-03T12:00:05Z",
       "decision_deadline":"2026-10-03T12:00:12Z",
       "server_now":"2026-10-03T12:00:05Z"
     }
   }
   ```

   Phases are `holding`, `review`, `cancelled`, `denied`, and `granted`.
   `decision_deadline` appears only once review begins; times are UTC RFC3339
   with optional fractional seconds. `server_now` is refreshed for each
   snapshot so clients can derive remaining time without matching wall clocks.
   Unsolicited snapshots omit `request_id`. Refusals include `ok:false`, `code`,
   and `error`; an invalidated request also carries its terminal snapshot.
   A successful takeover queues the ordinary unsolicited `type:"control"`
   acknowledgement with `ok:true` and `has_control:true` before `granted`.
   The server reserves queue capacity for that acknowledgement before changing
   ownership. A full requester control queue cancels the request and leaves
   the holder in control. Once queued, a later transport failure does not
   retroactively cancel the grant.
   Takeover progress itself never grants input authority.

5. The server answers each requested control change on the same ordered
   stream:

   ```json
   {
     "type":"control","request_id":17,"ok":true,
     "has_control":true,"control_session_id":"tab-7",
     "control_generation":9
   }
   ```

   A refusal keeps the same `type` and `request_id` and adds `code` and
   `error`; it also reports the authoritative `has_control`,
   `control_session_id`, and `control_generation`. A lease revocation that
   was not requested is an unsolicited `type:"control"` frame with no
   `request_id`; the displaced client remains a read-only observer.
   `revocation_reason` identifies `takeover` by another control session,
   `permission` loss (including protection), or generic `revoked` invalidation.
   The browser applies it only to its exact control session and generation;
   only `takeover` triggers the red control-border exit animation. Input is
   disabled immediately, without waiting for that animation. The browser changes
   its input state from acknowledged metadata, not the requested `write` bit.

6. Server sends one **text** geometry frame whenever the runtime accepts a
   changed shared PTY size:

   ```json
   {"type":"geometry","cols":132,"rows":43}
   ```

   The geometry frame is ordered before output drawn at that size. The PTY is
   the per-dimension minimum over attaches that impose geometry; a follower
   is excluded. A dashboard phone follows the acknowledged size, pans an
   oversized grid horizontally, and does not reflow the agent's screen.

7. The server re-checks authorization periodically. On an interactive attach,
   losing **steer** sends an unsolicited `type:"control"` notification on the
   same WebSocket, with `ok:false`, `has_control:false`,
   `revocation_reason:"permission"`, the authoritative `control_session_id`,
   and the exact `control_generation` that was revoked.
   The socket stays open as a read-only mirror; the dashboard disables input
   without replaying or reconnecting. A raw legacy (non-interactive) attach
   keeps the named close behavior: **1008**, reason `steer permission
   withdrawn`. Membership withdrawal closes every attach with **1008**, reason
   `membership withdrawn`, and stops reconnecting. A terminal session ending
   closes with **1000**, reason `session ended`; other failures use **1011**.

Closing the socket detaches; the run is unaffected. Changing away from a
dashboard run route closes that socket and unmounts its xterm, removing
Watching presence, control transport, and geometry participation. The browser
does not keep a fixed cache of recently visited terminal routes. A deliberate
reopen of the same mounted surface may use a bounded same-incarnation gap;
returning to an unmounted route starts from a compact snapshot.

CLI attachments do not request framing or `interactive`; they retain their raw
terminal stream and consume the ack-declared replay byte count before treating
following bytes as live. A CLI `screen:false` attach receives the retained raw
history, while a dashboard `screen:true` attach receives compact current-screen
state and never the archive. A writable CLI attach still discards input that
arrives before its announced replay has been written; it does not defer those
keystrokes.

#### Run shell tabs

`GET /ws/attach/<run_id>?shell=<tab>` opens a writable shell tab inside the
run container instead of attaching to the agent process. A shell tab always
requires **steer** permission and ignores the `write` value in the header;
there is no read-only shell mode. Tab names must match
`^[a-z0-9-]{1,32}$`, and at most four shells started by people can run in a
run at once; the agent's shells are counted apart.
The shell starts in `/workspace`. When it exits, the socket closes normally
with **1000** and the tab name is free to reopen with a fresh shell.
Closing the socket only detaches: the shell keeps running, still counts
toward that limit, and reconnecting the same tab name reattaches to
it. Every shell ends with the run's container. An unsuccessful initial TUI
agent exit returns the session to a login shell, so another installed agent
can use the same run checkout. A run shell can only be opened while the
container is live; finished runs expose their recorded terminal output but do
not create new shell tabs.

### `GET /ws/acp/<run_id>`

The session item log of a run with `acp: true` ([enhanced-runs.md](enhanced-runs.md)):
the items already logged after the client's cursor, then each new one as the
agent sends it. Any other run is refused with `-32602` and `run <run_id>
does not run its agent over ACP`. Every frame is JSON text.

1. Client sends one header frame. `write` asks for the run's control lease
   with the same fields a terminal attach uses (`takeover`,
   `release_control`); without it the socket only reads.

   ```json
   {"after_seq":412,"write":true,"control_session_id":"tab-1",
    "control_generation":0,"takeover":false,"release_control":false}
   ```

2. Server answers one ack frame. `seq` is the high-water mark the client
   holds once the `replay` frames that follow are applied; `live` says
   whether a session is running; `state` is its snapshot (turn in flight,
   queued prompts, pending requests with their options, mode, config
   options, commands, the agent's `auth` status and `auth_methods`,
   `prompt_images`: whether the agent accepts ACP image content, and
   `steering`: whether `run.inject` with `steer: true` can join a running
   turn, and `usage`: the agent's last reported context window use,
   `{"used":53000,"size":200000}`, absent until it reports one). A refusal
   carries `code` and `error` and closes 1008.

   The replay is at most 200 items. When `after_seq` is 0, past the end of
   the log, or more than 200 items behind, the server sends the newest 200
   and sets `oldest_seq` to the first seq sent: start the timeline there
   and page older items with `run.acp.history` (`before_seq` set to
   `oldest_seq`). Otherwise it sends exactly the items after `after_seq`
   and omits `oldest_seq`.

   If a nonzero cursor predates the retained window, the server sends an
   explicit reset frame after the ack, before replay. The ack's
   `truncated_before:true` identifies expired history independently of the
   200-item replay limit. Preserve that flag and authoritative pending
   requests through a same-epoch reset; a new epoch fences old state.
   History-page `oldest_seq` is the first retained public item (0 if empty),
   whereas ack `oldest_seq` is the first replayed item.

   ```json
   {"ok":true,"seq":431,"replay":19,"epoch":0,"live":true,
    "state":{"turn_in_flight":true,"queued":0,"pending":[],"mode":"auto","prompt_images":true},
    "has_control":true,"control_session_id":"tab-1","control_generation":7}
   ```

3. Server streams one frame per item, `{"seq":432,"item":{...}}`. An item
   over 32 KiB is cut to its identity fields with `"truncated":true`;
   `run.acp.item` returns it whole. A cursor past the end of the log (the
   log was replaced) is answered with `{"reset":true,"epoch":1}` first:
   drop what you hold and apply the replay that follows. Items are the
   `acphost.Item` kinds `message`, `thought`, `tool_call`, `plan`, `request`,
   `mode_change`, `config_options`, `commands`, `usage`, `auth_status`,
   `session_info`, `notice`, `turn_start`, `turn_end`, `background` and
   `reset`; skip kinds you do not know. `turn_start`, `turn_end` and
   `background` carry the fields for autonomous turns and Claude's
   background tasks described in
   [enhanced-runs.md](enhanced-runs.md#the-session-item-log), which also
   covers the ack `state` fields `background` and `tasks`.
4. Lease frames are the attach's: the client sends
   `{"type":"control","request_id":1,"write":true}` to take control or
   `{"type":"control","request_id":2,"control_generation":7}` to release it,
   and `{"type":"takeover",...}` frames for the timed handoff; the server
   answers with `control` and `takeover` frames, and sends a `control` frame
   with `revocation_reason` when another session takes the lease. The socket
   stays open as a viewer.

   A successful timed takeover sends the requester an unsolicited `control`
   frame with `ok:true`, `has_control:true`, its session id and the new
   generation before the `takeover` frame with phase `granted`. Use that
   acknowledgement to enable input; do not acquire the already-held lease
   again. The same acknowledgement is sent whether the holder accepts or
   the seven-second review expires.

The socket closes **1012** `session stream ended; resubscribe with
after_seq` when the session ends or restarts, when the run has no session
and one starts, or when the client falls 1024 items behind: reconnect with
your last `seq`. It closes **1008** when membership is withdrawn. Losing
Steer only drops the lease, as on an interactive attach: the server sends a
`control` frame with `revocation_reason:"permission"` and the socket stays
open as a viewer. A run accepts at most 32 of these sockets at once; the next is refused with
`-32003`.

### `GET /ws/terminal?tab=<tab>`

The member environment terminal uses the same binary-output and JSON-control
framing as run attaches. The `tab` query is `main` or a client-selected name
matching `^[a-z0-9-]{1,32}$`.

1. Client sends one text header with `cols` and `rows`, and `follow` where
   it means the same as it does for an attach. The gateway ensures the
   member's environment container and the requested shell.
2. The gateway answers
   `{"ok":true,"tab":"main","cols":120,"rows":40,"replay":4096}` with
   the session's live geometry or a JSON error followed by a close. For a
   framed dashboard terminal, the replay is a compact current-screen snapshot
   with bounded scrollback, not the shell's full recorded output. Each
   reconnect requests a fresh snapshot; environment-terminal attaches do not
   use the run attach's cursor-delta resume protocol.
   `replay` is the exact number of binary output bytes that follow the ack
   before live output. A binary frame may straddle that boundary, so the client
   splits it by the count. That declaration must be a finite, nonnegative safe
   integer; an invalid value becomes a final visible refusal rather than an
   allocation. The dashboard starts parsing frame-sized replay operations as
   they arrive, serially through public xterm write callbacks while the
   terminal surface stays hidden with CSS visibility. It does not retain replay
   until the full boundary or allocate a browser-sized buffer from the declared
   length. Only the slice containing the exact final replay byte is tagged
   `replay-end`; live output and geometry queue behind xterm backpressure.
   Terminal-generated replies and user input stay muted through the final
   replay callback, then input opens. The surface remains hidden for two paint
   turns and structural viewport restoration before reveal. At most six tabs
   may be active.
3. Output is binary. Input and resize are text frames, and the server's
   `geometry` frame arrives here the same way it does on an attach:

   ```json
   {"type":"input","data":"ls -la\r"}
   {"type":"resize","cols":132,"rows":50}
   ```

Closing the socket detaches without stopping the shell. A normal shell exit
closes with **1000**. Membership loss closes with **1008**. `terminal.status`
reports the running container's `image`, its `saved_image` when set, start
time, and active tabs; `terminal.stop` stops the container and deletes its tab
sessions while preserving the member home. `terminal.exec` runs one command
in the same container without a tab
([terminal.md](terminal.md#run-one-command)).
