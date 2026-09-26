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

`run` is the other query parameter the dashboard reads on first load, and it
leaves the address bar the same way. `?run=<run_id>` opens that run's
terminal as soon as the first hydration has the runs; a run the member cannot
see is ignored and the board stays. This is how both shells deliver an
`aether://run/<id>` deep link - the desktop shell (`desktop/main.js`) and the
Android app (`android/`) append it to the dashboard URL and load that -
and removing it is what stops a reload, or the re-hydration a reconnect runs,
from reopening a run the member has since left.

### Agent OAuth logins

When an agent prints an OAuth URL in the dashboard, click it. If the URL
contains an HTTP loopback callback, the dashboard opens a blank browser tab,
starts the local forward, and only then loads the authorization page. The
forward targets the run or environment terminal where the link appeared.

The server gateway has no `forward.start` verb to do that with, and the
callback port only exists on the machine that runs the forward, so opening
the link there would strand the login. Instead the dashboard leaves the link
unopened and shows a toast carrying it, the command to run on the machine
where the login will be finished, and a **Copy link** action:

```sh
aether forward run:<run-id> <port>     # a link that appeared in a run's terminal
aether forward terminal <port>         # a link in the environment terminal
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

## Design

Both transports use `internal/webgate` for HTTP and WebSocket dispatch. The
local gateway supplies an SSH-backed backend: reads and writes go through one
lazy, shared control-channel connection to the linked server, using the same
handlers as the server-hosted gateway. The server gateway supplies an
in-process backend for the member identified by Tailscale WhoIs. This keeps
the API and stream behavior independent of whether the browser is local or on
the tailnet.

For the local backend, when a call fails on transport (a server restart or a
dropped network), it redials once and retries once before surfacing `-32004`
(unavailable). `config.import` is not replayed: a lost response may follow
committed writes, so the failed attempt surfaces immediately. A subsequent
request can reconnect without replaying the import. A failure the server itself
answered passes through untouched as that `protocol.Error`. Streams get the
same treatment with a guard: a channel
that fails to open triggers a redial only when a keepalive shows the
connection is actually gone, because tearing down a healthy connection would
kill every live stream riding on it.

Every `-32004` carries a message prefix that says who has to fix it, and both
map to HTTP 503 as before. `network unreachable: ` means this machine could
not even attempt the connection (DNS resolution failed, or the kernel
reported no route or an interface down), so the user fixes their own
connectivity. `server unreachable: ` is everything else and is the default: a
refused connection, a dial timeout, a failed SSH handshake, or a wedged call,
where the server is the thing to check. The split stops at unambiguous cases
on purpose, since a refusal or a timeout cannot tell a stopped server from a
firewall, and a wrong guess sends the user to fix the wrong thing. Dial
failures are classified inside the shared dial path, so the `/ws/events`
refusal frame carries the same code and prefix as a `POST /api/v1` error.

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
| `GET` | `/ws/terminal?tab=<tab>` | persistent member environment terminal (WebSocket) |
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
separately at 8 MiB. File and configuration exceptions are listed below.

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

The first request starts at the newest retained output. Every response orders
its normalized text lines chronologically and returns `has_more`; when another
window is available it also returns `next_cursor`, which requests the next
older window. Search applies the literal query while scanning retained casts
and returns only matching normalized lines. Escape sequences and terminal
controls are interpreted into text rather than sent to the browser.

Line count is not the only bound. Each request also limits raw disk reads,
decoded bytes and events, elapsed time, cast segments, directory discovery,
and concurrent readers; searches have their own lower concurrency cap. A page
or search may therefore return fewer than the requested number of lines,
including none, with `has_more:true`. Continuing uses an older-page request
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
non-dashboard compatibility consumers that need the complete recording. The
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
| `-32600` invalid request: body not `application/json` | 415 |
| `-32000` not found | 404 |
| `-32002` invalid state, `-32003` conflict | 409 |
| `-32603` internal | 500 |

Param and result shapes are the ones in `internal/protocol` (`wire.go` and
the per-feature files), unchanged by this transport, and every call passes
the same capability and member-authorization checks regardless of transport.

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
separately. Active runs, pending runtime or mission work, configured schedules,
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
is a `RunResult` whose `run.archived_at` and `run.deletes_at` are set while
archived and absent otherwise; `deletes_at` is the date the server's
archive sweep deletes the run, on its first boot or hourly sweep at or
after that date (see
[failure-handling.md](failure-handling.md#disk-pressure)).
Both calls publish a `run.archived` event carrying the same two fields -
null on both means the run was restored - and a matching timeline note.

`run.relaunch` is another proxied control-channel method:

```sh
aether relaunch <run-id>
```

The equivalent gateway call is `POST /api/v1/run.relaunch` with
`{"run_id":"run_..."}` and a `RunResult` response. It is eligible only for a
TUI run in the **Done** state (`merged` or `abandoned`) whose `run.close`
operation retained its container (`reason` is `closed; retained container`)
and whose `--run-container-ttl` deadline has not passed. Relaunch resumes the
same run row in the same container and checkout; it does not create a new run
or container and does not perform a new launch or disk-floor admission.

Retention expiry is swept within at most one minute and reconciled on server
boot. Once expiry destroys the retained container, the call returns `-32002`
(invalid state, retained container unavailable), and the run cannot be
relaunched. An expired or otherwise unavailable retained run cannot be
relaunched; a row removed by `run.delete` instead returns not found. The
default `--run-container-ttl` is `168h` (7 days); negative values disable
retention, so a closed TUI run is unavailable to `run.relaunch` immediately.

### `GET /api/v1/capabilities`

```json
{"gateway":"local","methods":["*"],"ws":["events","attach","terminal"],
 "local":["daemon.install","daemon.status","env.harnesses","forward.start",
          "forward.status","forward.stop","git.identity","link.apply","link.repo",
          "link.status","link.switch","pull","pull.switch","repo.fast-forward",
          "repo.push","sync.start","sync.status","sync.stop",
          "update.apply","update.check","update.status","workspace.selection"],
 "version":"v1.2.3","commit":"abc1234"}
```

The server gateway answers the same shape with no `local` field because it
cannot run verbs on the browser's machine:

```json
{"gateway":"server","methods":["*"],"ws":["events","attach","terminal"],
 "version":"v1.2.3","commit":"abc1234"}
```

`methods` is `["*"]` because both transports dispatch every control-channel
method; `ws` lists the WebSocket surfaces served; `local` is the sorted
`/local/v1` verb list, absent where there are none. A client probes this
descriptor rather than hard-coding its transport. The SPA uses it to hide
machine-local onboarding, linking, repository and update controls while
leaving shared server surfaces, including Files and member configuration,
available through either gateway.

`version` and `commit` are the build serving the gateway, which is the only
way the SPA can learn what CLI it is running against - `server.info` answers
for the server. Both are absent on a gateway that predates them.

### Control-channel methods this gateway calls

The two `GET` endpoints above are backed by control-channel methods, as are
the file reads and the member and workspace writes below. `aether gui`
proxies these methods over SSH; the server gateway dispatches them in-process.
Both transports therefore expose the same API shape and authorization checks.

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
boundary; a client cannot supply an actor, run identity, mission authority,
or push grant. A transport failure is handled by the existing local-gateway
redial/retry policy, while a service denial, conflict, stale revision, or
unavailable owned source remains the server's protocol error.

| Method | Params | Result |
| --- | --- | --- |
| `run.patch` | `RunPatchParams` (`{"run_id":"...","from":"...","to":"..."}`; `from` and `to` optional) | `RunPatchResult` - the same JSON shape the patch `GET` answers |
| `server.disk` | none | `ServerDiskResult` - the same JSON shape the disk `GET` answers |
| `account.usage` | `{"account_member_id":"<member-id>","refresh":false}` (`account_member_id` may be empty for the caller's account) | `{"account_member_id":"<member-id>","providers":[{"provider":"claude"\|"codex","status":"ok"\|"stale"\|"unauthenticated"\|"unsupported"\|"unavailable"\|"error","windows":[{"id":"...","label":"...","used_percent":12.5,"resets_at":"2026-09-18T13:00:00Z"}],"plan":"...","updated_at":"2026-09-18T11:59:00Z","checked_at":"2026-09-18T12:00:00Z","retry_at":"...","error":"..."},...]}` |
| `files.tree` | `{"workspace_id":"...","run_id":"...","path":"src"}` (`run_id` optional; an empty, omitted or `"."` path is the root) | `{"entries":[{"name":"main.go","kind":"file","size":1234},...]}` |
| `files.read` | `{"workspace_id":"...","run_id":"...","path":"README.md"}` (`run_id` optional) | `{"content":"...","truncated":false,"binary":false,"size":1234,"revision":"<sha256>","writable":true}` |
| `files.write` | `{"workspace_id":"...","run_id":"...","path":"README.md","content":"...","revision":"<sha256>"}` (`run_id` optional; an empty `revision` creates a new file) | the same `FileRead` shape as `files.read`, for the saved bytes |
| `files.diff` | `{"run_id":"...","path":"README.md"}` | `{"patch":"...","truncated":false}` |
| `config.roots` | `{}` | `{"roots":[{"harness":"claude","path":"~/.claude","runtime_ignores":["projects/","shell-snapshots/","statsig/","todos/","file-history/","history.jsonl","daemon/"]}]}` |
| `config.tree` | `{"harness":"claude","path":"."}` (`path` may be omitted, empty, or `"."` for the root) | `{"entries":[{"name":"settings.json","kind":"file","size":1234},...]}` |
| `config.read` | `{"harness":"claude","path":"settings.json"}` | `{"content":"...","truncated":false,"binary":false,"size":1234,"revision":"<sha256>","writable":true}` |
| `config.write` | `{"harness":"claude","path":"settings.json","content":"...","revision":"<sha256>"}` (`revision` is empty only for a new file) | the same `FileRead` shape as `config.read`, for the saved bytes |
| `config.import` | `{"harness":"claude","files":[{"path":"settings.json","content_base64":"...","mode":420}]}` | complete: `{"harness":"claude","files":1,"bytes":12,"excluded":[{"path":"notes.md","reason":"secret","detail":"..."}]}`; an incomplete write after at least one file is committed adds `"error":"...failed relative path...: ..."` and `"imported_paths":["settings.json"]` |
| `terminal.status` | none | `TerminalStatusResult` - whether the member environment is running, its `image`, optional `saved_image`, start time, and active tabs |
| `terminal.stop` | none | empty result; stops the member environment and its tabs |
| `terminal.image` | `TerminalImageParams` (`{"run_id":"<run-id>","content":"<base64-original-image-bytes>"}`; `run_id` optional) | `TerminalImageResult` (`{"path":"/home/<account>/.aether/terminal-images/image-<random>.png"}`) - absolute path in the target container |
| `env.save` | none | `EnvSaveResult` (`{"image":"aether/member-<id>:<unix-seconds>"}`) - commits the running environment terminal as the member's image |
| `env.reset` | none | empty result; stops the environment, forgets and removes the saved image |
| `workspace.origin` | `WorkspaceOriginParams` (`{"workspace_id":"...","origin":"https://github.com/acme/app.git"}`; `origin` empty clears it) | `WorkspaceOriginResult` - the workspace with its new `origin`, the upstream every new run checkout's `origin` remote points at |
| `workspace.mirror.status` | `WorkspaceMirrorParams` (`{"workspace_id":"..."}`) | `WorkspaceMirrorResult` - whether mirroring is enabled, source, branch, status, observed and accepted commits, check times, public key, and safe warning/error fields; no private key or server path |
| `workspace.mirror.configure` | `WorkspaceMirrorConfigureParams` (`{"workspace_id":"...","source_url":"https://github.com/acme/app.git","branch":"main","auth":"public"\|"deploy-key","known_hosts":"..."}`) | `WorkspaceMirrorResult`; deploy-key configuration includes only the public key and safe installation warning |
| `workspace.mirror.refresh` | `WorkspaceMirrorParams` (`{"workspace_id":"..."}`) | `WorkspaceMirrorResult` after fetching the configured source branch |
| `workspace.mirror.adopt` | `WorkspaceMirrorAdoptParams` (`{"workspace_id":"...","generation":7}`) | `WorkspaceMirrorResult` after explicitly accepting the retained candidate |
| `workspace.mirror.disable` | `WorkspaceMirrorParams` (`{"workspace_id":"..."}`) | `WorkspaceMirrorResult` with `enabled:false`; the workspace becomes local-only |
| `github.connect` | none | `GitHubConnectResult` (`{"login":"...","signing_key":"ssh-ed25519 ...","fingerprint":"SHA256:..."}`) - finishes the GitHub connection for the calling member |
| `github.probe` | none | `GitHubProbeResult` (`{"status":"ok","version":"2.100.0","minimum":"2.81.0","detail":"gh version 2.100.0 (2026-09-03)\nhttps://github.com/cli/cli/releases/tag/v2.100.0","image":"ghcr.io/3xdevops/aether-standard:v0.2.0-alpha.7"}`) - the gh in the calling member's environment terminal |

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

`config.roots`, `config.tree`, `config.read`, `config.write`, and
`config.import` always address the authenticated member's own persistent home.
They require **Launch**; an administrator cannot select another member with an
extra request field. `config.write` has the same explicit-save and revision
rules as `files.write`, while `config.import` installs an explicitly selected
directory into that home and may be used repeatedly.
The permanent **Configuration** route appears in shared navigation and the
command palette, and as an action on **Agents**, whenever `config.roots` and
`config.import` are advertised. It works through both gateways without a
workspace or onboarding prerequisite; local onboarding is another optional
entrypoint to the same importer. A server-hosted page can read local files
explicitly selected in the browser directory picker.
All runs using the member's account and the environment terminal mount one
shared read-write persistent HOME. A file edit, configuration import, or
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

The browser uses the selected root's `runtime_ignores` metadata before
reading or uploading any bytes. `runtime_ignores` contains exact,
case-sensitive root-relative paths and component prefixes; trailing slashes
are ignored for matching. These lists are per harness, so a runtime file
ignored for Claude is not implicitly ignored for OMP or a member-defined
custom harness. Known credential names wherever they occur in a path, and
every basename ending in `.pem`, remain filtered by the existing
destination-independent credential policy. The browser keeps raw local file
handles so it can recompute an import when the destination changes, and
cannot change destinations during import. After a result, the user can start
another directory selection or choose **Open remote files** to navigate to the
existing **Files** editor.
The preview lists eligible paths and policy exclusions without reading file
bytes. Directories have no file-count or decoded-total ceiling. The browser
reads and encodes only the current batch: at most 2,000 files and a target of
20 MiB decoded. A larger individual file is sent alone. Each `config.import`
request permits at most 2,000 files and 64 MiB decoded, with a 64 MiB per-file
ceiling matching configuration editing. These bounds limit a request, not the
directory. Oversized files and invalid paths block preparation rather than
offering to import a truncated subset.
Preparation checks canonical destination keys across the whole selection, so
root-prefixed aliases cannot overwrite each other in separate batches.
Accepted bytes are uploaded and server-scanned, so a secret finding does not
mean those bytes stayed local. Empty and binary regular files are preserved;
new browser-imported files use `0644`, while overwrites preserve existing
remote modes. The browser cannot preserve source executable bits or symlinks.
The server rejects unsafe paths, symlink components, hardlinks, and
non-regular destinations.

The result's `files` and `bytes` count
accepted files only; `excluded` reports server-side credential, ignore,
secret, or safety exclusions. Explicit CLI profile `push`, `status`, and
`rollback` remain separate manual operations; neither browser imports nor
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
successful one: the dashboard warns that copied files remain and lets the user
inspect **Files** before retrying. The optional `error` and `imported_paths`
fields are absent from complete results and from failures before any file is
committed.

If the SSH/RPC call fails without such a result, the outcome is unknown: the
request may have copied some files before the response was lost, so the
dashboard says that some files may remain and directs the user to inspect
**Files** before retrying. Cancellation can prevent the response from being
delivered; the protocol does not claim a stronger delivery guarantee.

Across batches the dashboard accumulates confirmed counts, paths, and server
exclusions. A read failure or server-reported partial failure stops subsequent
batches. A lost response leaves only that request's outcome unknown; earlier
confirmed writes remain reported. Navigation retains owner-scoped progress and
results. Before each request, including after file reads, the importer checks
the authenticated identity and stops if it changed. An in-flight request may
still complete for its original owner; the new identity cannot see its result.

The generic HTTP proxy caps ordinary `/api/v1` JSON bodies at 1 MiB,
`files.write` and `config.write` at 385 MiB to allow worst-case JSON escaping,
and `config.import` at 96 MiB. The 64 MiB file and per-request import limits
remain authoritative after JSON decoding. The SSH control-channel line cap is
96 MiB, allowing base64 import requests without unbounded framing.
The HTTP gateway admits at most two imports at once, before reading their
bodies, and retains admission through backend processing and the response.
Excess requests receive HTTP 503 (`configuration import capacity is busy;
retry later`) without being read. An import body must make progress within
30 seconds and finish within 15 minutes; expired reads return HTTP 408.
These are transfer protections, not limits on the selected directory.
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
those events to know when to ask again and which interval to ask for.

With no query params the server renders the run checkout's whole diff against
the fork point its identity record pins (the `aether.base` commit), covering
committed work, uncommitted edits and untracked files alike - the same set of
changes a `run.diff` snapshot counts. Rendering leaves the checkout alone: the
worktree is staged into a scratch index with its own scratch object directory,
so nothing under the checkout's `.git` - not even the loose objects staging
hashes - is ever written, and the scratch files are deleted once the patch is
rendered.

`from` and `to` render one interval instead. Both take a snapshot tree id off
a `run.diff` event - its `parent_tree` and its `tree` - and the answer is the
diff between those two trees, which is what the run changed in that interval.
Passing one without the other is an invalid-params error. An id has to be a
full object id, has to resolve against that run's own object database and no
other, and has to name a tree: a commit id would otherwise peel to its tree
and render a diff the timeline never offered.

```json
{"run_id":"run_01H...","base":"9f2c1e...","patch":"diff --git a/main.go b/main.go\n...","truncated":false}
```

- Visibility is `run.get`'s, applied by calling it: a member who could not
  read the run over the control channel gets that method's refusal here,
  unchanged.
- `base` is what the patch is measured from: the fork-point commit on a
  cumulative render, the `from` tree on an interval one.
- `patch` contains the complete rendered diff up to 64 MiB. An oversized
  response sets `truncated:true` and ends at a whole line. The dashboard
  renders the response inline; it does not serve repositories.
- `503` with `-32004` when the server has no git engine wired, when the run
  has no checkout left to diff (it finished and was cleaned up), when a
  requested tree is no longer on disk - a run's snapshot objects are removed
  with its checkout, so its intervals go when the checkout does - or when
  rendering ran past the engine's 30s ceiling - the same bound a diff
  snapshot's git work gets, because staging re-hashes every untracked file
  and a worktree holding a large un-ignored tree would otherwise be
  unbounded.

### `GET /api/v1/disk`

Usage of the filesystem holding the server's data directory, for the status
bar's disk gauge:

```json
{"used_bytes":21474836480,"total_bytes":107374182400,"free_bytes":85899345920,
 "worktree_bytes":3221225472,"transcript_bytes":104857600,"database_bytes":52428800,
 "repo_bytes":8589934592}
```

`used_bytes` and `total_bytes` describe the whole filesystem - the gauge
answers "is the disk filling up", which is not a question about Aether's own
footprint. `free_bytes` is what an unprivileged writer can still claim, which
is the number the scheduler's free-space floor is checked against, and is
smaller than `total - used` wherever the filesystem reserves blocks.

The last four are the directories that grow without bound and are the only
part an operator can act on: run checkouts (garbage-collected after their
TTL), transcripts, the SQLite file the persisted event log shares with the
store, and `repos/`, the bare repo behind each workspace. The event log has
no file of its own to measure, so the database line covers both. The bare
repos keep every push, every run branch and the reflogs `internal/gitengine`
turns on, and nothing reclaims them. `repo_bytes` is absent on servers
predating the component, and the dashboard drops the line rather than
showing a zero.

The components do not overlap. A run checkout is a `git clone --local` of
its workspace repo, so its object files are hard links to bytes already in
`repos/`: the walk indexes by device+inode and charges each one to the
first tree that reaches it, walking `repos/` first. `repo_bytes` therefore
holds the shared objects, and `worktree_bytes` is what reclaiming that
checkout would actually free. A component that cannot be read contributes
zero rather
than failing the whole reading. Measurement lives in `internal/disk`, shared
with the scheduler's floor so the gauge and the refusal can never disagree
about the same disk.

`protocol.ServerInfoResult` is shared with the CLI and frozen, and the number
is of no use to a terminal client, so it is read here instead of being added
to `server.info`. Any member may read it, because it says how much room the
deployment has left, not what anyone is running. `503` with `-32004` when the
server was not told where the data directory is, or the platform has no
`statfs` (the server ships for linux; the read refuses rather than reporting
zero anywhere else).




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
server stores the generated file in the target account member's persistent
home and returns its absolute path as mounted at `$HOME` in that container.
The path and filename are server-generated; callers cannot choose a host path
or ask this method to read an arbitrary path.

- `run.patch` returns the complete rendered diff up to 64 MiB. An oversized
  response sets `truncated:true` and ends at a whole line.
  `from` and `to` select one interval here exactly as they do on the `GET`.
- The read methods answer `-32004` (unavailable) when the read cannot be
  served: `run.patch` when diff rendering is not enabled (no git engine
  wired), when the run has no checkout left to diff, or when a requested tree
  is gone (`run.patch: that snapshot's tree is no longer on disk`),
  `server.disk` when the server was not told where the data directory is or
  the filesystem holding it could not be read, and `files.tree`,
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
  the server. The dashboard's Agents step calls this after the member
  finishes `gh auth login` in the terminal dock; see
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
| `link.status` | `{}` | `{"linked":bool,"server_configured":bool,"addr":"...","user":"...","repo":"...","links":[{"name":"...","addr":"...","repo":"..."}],"active":"..."}` (`links` is present whenever a named profile is saved, `active` only when the gateway runs on one; a profile's `repo` is omitted when it records no clone of its own and inherits the top-level one; `server_configured` reports a configured server even when no repository is linked) |
| `link.switch` | `{"name":"..."}` | always `-32002` (invalid state): `restart aether gui --server <name> to switch servers` |
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
| `env.harnesses` | `{}` | `{"harnesses":[{"name":"claude","installed":bool},...],"searched":["/usr/local/bin",...],"warning":"...","repo_path":"..."}` - the setup-capable harnesses in order, with whether each executable is on this machine's `PATH`. The verb first widens the gateway's `PATH` from your login shell (`$SHELL -l -i`, bounded to 5 seconds), so agents installed through a shell profile or since the gateway started are found; `searched` is the resulting `PATH` as a list of folders (always present, may be empty); `warning` is present only when the login shell could not be asked, carrying that error verbatim (the standard folders `/usr/local/bin`, `/opt/homebrew/bin`, `~/.local/bin`, and `~/.bun/bin` were still checked); `repo_path` is the repository folder the saved link config knows, present only when exactly one is known, for prefilling the wizard's from-repo folder input |
| `forward.start` | `{"target":"run:<run-id>|terminal","port":1455}` | `{"target":"run:<run-id>|terminal","port":1455,"local_port":1455,"state":"active"}`; idempotent for the same target and port |
| `forward.stop` | `{"target":"run:<run-id>|terminal","port":1455}` | `{"target":"run:<run-id>|terminal","port":1455,"state":"stopped"}` |
| `forward.status` | `{}` | `{"forwards":[{"target":"run:<run-id>|terminal","port":1455,"local_port":1455,"conns":1}]}` sorted by target, then port |
| `update.check` | `{"refresh":bool}` (optional; `true` skips the cached release lookup) | `{"cli":{...},"server_version":"v1.2.9","server_behind":bool,"server_error":"...","supervised":bool,"shell_build_error":"...","cli_path":"/usr/local/bin/aether","install_method":"direct"\|"admin-prompt"\|"manual"}` (`server_error` only when the server did not answer; `shell_build_error` only when the last in-app desktop rebuild failed; `cli_path` and `install_method` absent when the binary could not be probed) |
| `update.apply` | `{}` | `{"updated":["/usr/local/bin/aether"],"version":"v1.3.0","restarting":bool,"rebuilding":bool,"note":"...","restart_command":"..."}` (`restart_command` only when `aether-server` was replaced too) |
| `update.status` | `{}` | `{"phase":"packaging","lines_tail":["..."],"error":"..."}` - the desktop-app rebuild `update.apply` started (`error` only when `phase` is `error`) |
| `workspace.selection` | `{}` reads; `{"workspace_id":"..."}` saves; an empty ID clears | `{"workspace_id":"..."}` |

`link.apply` saves the link and swaps the gateway connection in place, so
subsequent API and WebSocket requests use the new server without a restart. If
no SSH key is offered and the server requires one, it may create
`~/.ssh/id_ed25519` and its `.pub` file.

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
  must carry (the onboarding wizard sends the one just picked). Without
  it the workspace resolves exactly like `aether link --repo`: a single
  workspace resolves implicitly; none or several answers `-32002`
  (invalid state) and is resolved server-side or with the CLI's
  `--workspace` flag first.
- `workspace.mirror.*` methods are admin-only. `configure` receives the
  verified source URL, branch, auth mode, and (for generic SSH) the
  `known_hosts` file contents; the server generates and stores any deploy key.
  Results expose source, branch, status, observed/accepted commits, check
  times, and the public key only. The dashboard's Workspace **Source control**
  panel calls these same methods.
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
  `dev`, `disabled`, `can_self_update` and `checked_at`
  ([install.md](install.md#upgrading)). The gateway resolves the latest
  release at most once an hour and serves the cached answer in between, so
  a page load never costs a request to GitHub. `refresh: true` skips the
  cache and dials, and the answer it resolves replaces the cached one; the
  dashboard sets it on the read behind the Update button, which names the
  release about to be installed. A refresh that cannot reach GitHub is that
  caller's error - `-32004`, `check for releases: <the lookup's own
  message>` - rather than the cached answer, and it leaves any cached
  success in place. A failed lookup is cached for five minutes, so an
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
  that build's own error.
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
  at that moment rather than what was newest when the banner was drawn. A
  lookup that fails is the call's error; no cached tag stands in for it. It
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
  (`unpacking`, `fetching node`, `installing dependencies`, `packaging`,
  `installing`), then `done` or `error`. `lines_tail` carries the last 20
  lines of the build's own output, and `error` the build's own message. A
  gateway that comes up after a rebuild answers `idle`: the build belonged
  to the process that exited.
- A supervised gateway exits only once the rebuild ends: **75** on success,
  so the shell relaunches onto the new app, and **0** on failure, so the
  shell respawns the sidecar on the new CLI. A failed build is also written
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
    set, Windows, and a running build that is already the newest release
    (downloading it over itself would report success for work that changed
    nothing, and restart the app for nothing).
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

Every live socket - `events`, `attach`, and `terminal` - is pinged by the
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
   {"type":"control","request_id":19,"write":true,"takeover":true,
    "control_generation":8}
   ```

   `control` changes the lease on this same WebSocket; it does not reconnect
   or replay. The optional `takeover:true` explicitly displaces the current
   controller. Include the current `control_generation` when fencing a
   release, takeover, or input. Read-only input is ignored and stale input is
   rejected rather than reaching the PTY.

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
   `request_id`; the displaced client remains a read-only observer. The
   browser changes its input state only from this acknowledged metadata, not
   from the requested `write` bit.

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
   same WebSocket, with `ok:false`, `has_control:false`, the authoritative
   `control_session_id`, and the exact `control_generation` that was revoked.
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
`^[a-z0-9-]{1,32}$`, and each run can have at most four active shell tabs.
The shell starts in `/workspace`. When it exits, the socket closes normally
with **1000** and the tab name is free to reopen with a fresh shell.
Closing the socket only detaches: the shell keeps running, still counts
toward the four-tab cap, and reconnecting the same tab name reattaches to
it. Every shell ends with the run's container. An unsuccessful initial TUI
agent exit returns the session to a login shell, so another installed agent
can use the same run checkout. A run shell can only be opened while the
container is live; finished runs expose their recorded terminal output but do
not create new shell tabs.

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
sessions while preserving the member home.
