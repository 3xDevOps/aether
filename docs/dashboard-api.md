# Dashboard HTTP/WS API

The dashboard gateway (`internal/dashboard`) is the browser-facing
transport of the Aether server. It serves the embedded SPA and bridges
browser clients onto exactly the same control-channel handlers, event
bus, and PTY host the SSH transport uses: one service layer, two
transports.

The gateway runs only when the server was started with `--dashboard-port`
(loopback listener) or `--dashboard-addr` (direct exposure); with neither
flag the service is not built and no HTTP port exists.

## Identity and tokens

The SSH key stays the only identity system. HTTP has no login: every API
and WebSocket request carries a **bearer token minted over the
authenticated SSH control channel**, and acts with exactly the authority
of the member it was minted for - same role, same capability checks, same
pending-member gate.

Control-channel methods (SSH, not HTTP):

| Method | Params | Result |
| --- | --- | --- |
| `dash.token.mint` | `{"ttl_seconds":43200}` (optional) | `{"token":"...","expires_at":"RFC3339","url":"http://..."}` |
| `dash.token.revoke` | `{"token":"..."}` | `{}` |

- TTL defaults to 12h and is capped at 24h. There is no lower bound: a
  caller that asks for one second gets a one-second token.
- Minting is rate limited per member (burst 8, refill one per minute);
  past that the call fails `-32003` (conflict). One mint per `aether dash`
  never notices, and a scripted loop cannot grow the gateway's in-memory
  token table without bound.
- `url` is set only when the server exposes the dashboard directly
  (`--dashboard-addr`). Clients reaching the gateway through the
  `aether dash` SSH forward build their own
  `http://127.0.0.1:<forwarded-port>/?token=<token>`. `aether dash` does
  exactly that, opens it, and revokes the token when it exits (on `SIGINT`,
  `SIGTERM`, or `SIGHUP` - a closed terminal revokes too). `aether dash
  --url` prints the URL instead of opening a browser. Against a directly
  exposed server, interactive `aether dash` prints the server's own URL,
  opens the browser, and stays running so the same exit revoke applies.
- **Only direct-exposure `aether dash --url` leaves a token unrevoked.**
  With no forward to hold open, that path prints the server's own URL and
  returns immediately; a printed URL is for scripting and meant to outlive
  the process, so there is nothing to revoke on. That token stays valid
  until it expires, and no `aether` command revokes a token afterwards, so
  the expiry is printed alongside the URL. A server restart is the other
  way to clear it. (Forwarded `--url` keeps the forward - and so the exit
  revoke - alive.)
- A member may revoke only its own tokens. Tokens live in memory: a
  server restart invalidates every token.
- Authority is re-checked on sockets that are already open, not just on
  new requests. A live `/ws/events` or `/ws/attach` re-runs its gates every
  few seconds and is closed with WebSocket status `1008` (policy
  violation) when one stops holding: the token revoked or expired, the
  member removed or un-approved, or - on a write attach - the steer
  capability withdrawn (the run protected, the session set to
  `admins_only`, the run handed off). The close reason names which. On both
  sockets the watch runs from the moment the socket is accepted - before the
  header frame arrives - and the token is re-resolved when the header does,
  so a revocation can never be converted into a live attach or subscription.
  Only a definitive refusal closes a socket: a transient failure of the
  check itself (a store hiccup, a cancelled context) is not treated as lost
  authority. A client that wants
  to keep watching reconnects; a write attach that lost steer reconnects
  without `"write"`.
- Both methods answer `-32004` (unavailable) when the gateway is off.

Presenting the token:

- HTTP: `Authorization: Bearer <token>`.
- WebSocket: `Authorization: Bearer <token>` or, because browsers cannot
  set headers on a WebSocket handshake, `?token=<token>`.

Missing, unknown, or expired tokens get `401` with the standard error
body. Static files are served without a token - the SPA bundle is not
secret and needs to load before it can present one.

## Routes

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/` and any other non-API path | the SPA (fallback to `index.html`) |
| `POST` | `/api/v1/<rpc.method>` | one control-channel method call |
| `GET` | `/api/v1/run/<run_id>/patch` | the run's diff as unified patch text |
| `GET` | `/api/v1/disk` | data-directory disk usage |
| `GET` | `/ws/events` | event subscription (WebSocket) |
| `GET` | `/ws/attach/<run_id>` | PTY attach (WebSocket) |

Cross-origin WebSocket handshakes are rejected; the SPA is served from
the same origin as the API.

### Static serving

Anything that is not `/api/` or `/ws/` is served from the embedded
`web/dist`. Unknown paths fall back to `index.html` so client-side
routing works on a hard refresh. An `/api/` or `/ws/` path hit with the
wrong method is the exception: it answers `405` with a JSON error body
instead of falling through to the SPA, so a wrong-verb client bug cannot
masquerade as a `200`. If the binary was built without an SPA
(`web/dist` has no `index.html`), every such request answers `503` with
`dashboard not built` in plain text rather than a blank page.

### `POST /api/v1/<method>`

The path segment after `/api/v1/` is the JSON-RPC method name, dots
included: `POST /api/v1/run.list`. The request body is the method's
`params` object (an empty body means no params).

**The transport carries an allowlist, not the whole method map.** Anything
not listed answers `403` with code `-32001`, whether or not it exists on
the SSH control channel:

| Group | Methods |
| --- | --- |
| Core | `server.info`, `workspace.list`, `session.list`, `session.get`, `member.list` |
| Runs | `run.launch`, `run.list`, `run.get`, `run.kill`, `run.pause`, `run.resume`, `run.inject`, `run.close`, `run.handoff` |
| Approvals and presence | `approval.list`, `approval.decide`, `presence.roster`, `presence.heartbeat` |
| Timeline, cost, conflicts | `session.timeline`, `cost.report`, `budget.get`, `run.overlaps` |
| Templates | `template.list`, `template.launch` |

Everything else stays SSH-only, because a bearer token travels in a URL
and is far easier to capture than an SSH key: methods that issue or widen
a credential (`member.invite`, `member.approve`, `member.remove`,
`dash.token.*`), that replace what is mounted into run containers
(`profile.*`), or that administer the deployment (`workspace.add`,
`session.new`, `session.settings`, `budget.set`, `template.save`,
`template.delete`, `schedule.*`) must not be reachable with a stolen
dashboard token, which the allowlist guarantees even though the token
otherwise carries its member's full authority.

Allowlisted methods still pass every capability check the SSH transport
applies, and param and result shapes are the ones in `internal/protocol`
(`wire.go` and the per-feature files), unchanged by this transport.

Success is `200` with the method's result object as the whole body:

```http
POST /api/v1/run.list
Authorization: Bearer <token>
Content-Type: application/json

{"active_only":true}
```

```json
{"runs":[{"id":"run_01H...","session_id":"ses_01H...","status":"running", ...}]}
```

Failure is a non-2xx status with the JSON-RPC error object wrapped:

```json
{"error":{"code":-32001,"message":"run.kill: permission denied"}}
```

Status mapping (the code is the authority; the status is a convenience):

| JSON-RPC code | HTTP |
| --- | --- |
| `-32700` parse, `-32600` invalid request, `-32602` invalid params | 400 |
| unauthenticated (no/expired token) | 401 |
| `-32001` denied | 403 |
| `-32000` not found | 404 |
| `-32002` invalid state, `-32003` conflict | 409 |
| `-32603` internal | 500 |
| `-32004` unavailable | 503 |

### `GET /api/v1/run/<run_id>/patch`

The one endpoint that is not an RPC method, because patch text is a read of a
working tree rather than a control-channel call. `run.diff` events carry
per-file stats only and no patch text is stored anywhere in the server, so the
dashboard's diff timeline reads the text here and uses those events to know
when to ask again.

The gateway renders the run checkout's whole diff against the fork point its
identity record pins (the `aether.base` commit), covering committed work,
uncommitted edits and untracked files alike - the same set of changes a
`run.diff` snapshot counts. Rendering leaves the checkout alone: the worktree
is staged into a scratch index with its own scratch object directory, so
nothing under the checkout's `.git` - not even the loose objects staging
hashes - is ever written, and the scratch files are deleted once the patch
is rendered.

```json
{"run_id":"run_01H...","base":"9f2c1e...","patch":"diff --git a/main.go b/main.go\n...","truncated":false}
```

- Visibility is `run.get`'s, applied by calling it: a member who could not
  read the run over the control channel gets that method's refusal here,
  unchanged, and nothing is rendered. The 401 / 403 / 404 rules above hold.
- `truncated` reports that the diff outgrew the 512 KiB ceiling; `patch` then
  ends at the last whole line that fit. Read the run branch over git for the
  rest - the dashboard renders diffs, it does not serve repositories.
- `503` with `-32004` when the server has no git engine wired to the gateway,
  when the run has no checkout left to diff (it finished and was cleaned up),
  or when rendering ran past the engine's 30s ceiling - the same bound a diff
  snapshot's git work gets, because staging re-hashes every untracked file and
  a worktree holding a large un-ignored tree would otherwise be unbounded.

### `GET /api/v1/disk`

Usage of the filesystem holding the server's data directory, for the status
bar's disk gauge:

```json
{"used_bytes":21474836480,"total_bytes":107374182400,"free_bytes":85899345920,
 "worktree_bytes":3221225472,"transcript_bytes":104857600,"database_bytes":52428800}
```

`used_bytes` and `total_bytes` describe the whole filesystem - the gauge
answers "is the disk filling up", which is not a question about Aether's own
footprint. `free_bytes` is what an unprivileged writer can still claim, which
is the number the scheduler's free-space floor is checked against, and is
smaller than `total - used` wherever the filesystem reserves blocks.

The last three are the directories that grow without bound and are the only
part an operator can act on: run checkouts (garbage-collected after their
TTL), transcripts, and the SQLite file the persisted event log shares with
the store. The event log has no file of its own to measure, so the database
line covers both. A component that cannot be read contributes zero rather
than failing the whole reading. Measurement lives in `internal/disk`, shared
with the scheduler's floor so the gauge and the refusal can never disagree
about the same disk.

The other endpoint that is not an RPC method. `protocol.ServerInfoResult` is
shared with the CLI and frozen, and the number is of no use to a terminal
client, so it is read here instead of being added to `server.info`. Any member
holding a token may read it - membership is re-validated like every other
endpoint, so a removed or pending member's token gets `403` - because it says
how much room the deployment has left, not what anyone is running. `503`
with `-32004` when the gateway was not told
where the data directory is, or the platform has no `statfs` (the server
ships for linux; the endpoint refuses rather than reporting zero anywhere
else).

### `GET /ws/events`

Same subscription semantics as the SSH events subsystem, so a client that
lost its socket resumes without gaps.

1. Client sends one **text** frame: a `SubscribeRequest`. The header must
   arrive within 10 seconds or the socket is closed; frames sent after it
   are discarded, so an application-level keepalive does not tear the
   stream down.

   ```json
   {"session_id":"","run_id":"","types":[],"replay":true,"after_seq":412}
   ```

2. Server answers one **text** frame: `{"ok":true}`, or
   `{"ok":false,"code":-32004,"error":"..."}` followed by a close.
3. Server then streams one **text** frame per event, each a `protocol.Event`:

   ```json
   {"id":"evt_01H...","seq":413,"time":"2026-08-14T10:00:00.123456Z",
    "session_id":"ses_...","run_id":"run_...","actor_id":"mem_...",
    "type":"run.status","payload":{...}}
   ```

Reconnect contract: track the highest `seq` you have seen and resubscribe
with `"replay":true,"after_seq":<last seq>`. If the server's per-client
buffer overflows it closes with code **4000** (`event backlog dropped`) -
that close is the signal to resubscribe from your last `seq`, not an
error to surface.

### `GET /ws/attach/<run_id>`

PTY attach. Output is binary, control is JSON, matching the terminal
view's needs.

1. Client sends one **text** frame with the attach header (the run comes
   from the path). The header must arrive within 10 seconds or the socket
   is closed. Write is opt-in - the header of a read-only mirror is just
   `{}`:

   ```json
   {"write":true,"cols":120,"rows":40}
   ```

2. Server answers one **text** frame: `{"ok":true,"cols":120,"rows":40}`,
   or `{"ok":false,"code":-32001,"error":"..."}` followed by a close.
   A write attach is refused with `-32001` unless the member holds the
   **steer** capability on that run; dropping `"write"` always works for
   a member who can see the run. An unknown run is refused with `-32000`,
   a run with no live terminal with `-32004`.
3. Server then streams terminal output as **binary** frames.
4. Client sends **text** control frames:

   ```json
   {"type":"input","data":"ls -la\r"}
   {"type":"resize","cols":132,"rows":50}
   ```

   Control frames from a read-only attach are ignored. Only write-capable
   attaches affect the shared terminal geometry (Wave 1 contract). Client
   frames are capped at 64 KiB; the SPA splits larger input (a paste)
   across several ordered `input` frames.

Closing the socket detaches; the run is unaffected.

## Related

- `internal/protocol` - the wire types, shared with the CLI.
