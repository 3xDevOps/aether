# The local gateway (`aether gui`)

`aether gui` serves the dashboard from the user's own machine
(`internal/localgw`): the same embedded SPA as the server's dashboard
gateway, the same `/api/v1` shape, proxied over the machine's SSH
connection to the linked server. Because the identity is the member's own
SSH key rather than a minted dashboard token, the **full control-channel
method map** is reachable - no allowlist - plus the client-machine verbs
under `/local/v1` that only a machine with the user's repository and SSH
key can offer. The security stances are in
[security.md](security.md#the-local-gateway); the remote gateway and the
shared WebSocket framing are in [dashboard-api.md](dashboard-api.md).

## Running it

```sh
aether gui             # bind an ephemeral loopback port, print the URL, open a browser
aether gui --port 8090 # bind a fixed loopback port
aether gui --url       # print the URL instead of opening a browser
aether gui --json      # print one JSON line, then keep serving
```

The gateway binds `127.0.0.1` only - there is no exposure flag - and mints
a per-process bearer token that every request must carry (as
`Authorization: Bearer`, or `?token=` on WebSocket handshakes and the
initial browser tab). The printed URL is
`http://127.0.0.1:<port>/?token=<token>`. The process serves until
`SIGINT`, `SIGTERM`, or `SIGHUP`; the token dies with it.

`--json` prints exactly one line and then serves:

```json
{"url":"http://127.0.0.1:43871/?token=...","addr":"127.0.0.1:43871"}
```

That line is the contract with the desktop shell sidecar, which spawns
`aether gui --json`, parses the line, and renders the SPA itself.

## Design

The gateway holds no server code: every read and write is a
control-channel call proxied over one SSH connection to the linked server,
through the same `internal/cli` client the terminal commands use. One
`Backend` interface covers the whole surface - `Call` for methods, and a
fresh subsystem channel per WebSocket for events, attach, shell, and sync
- so the HTTP handlers never know they are riding SSH.

The connection is dialed lazily on first use and shared. When a call fails
on transport (a server restart, a dropped network) the backend redials
once and retries once before surfacing `-32004` (unavailable); a failure
the server itself answered passes through untouched as that
`protocol.Error`. Streams get the same treatment with a guard: a channel
that fails to open triggers a redial only when a keepalive shows the
connection is actually gone, because tearing down a healthy connection
would kill every live stream riding on it.

## Routes

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/` and any other non-API path | the SPA (fallback to `index.html`) |
| `POST` | `/api/v1/<rpc.method>` | any control-channel method, proxied over SSH |
| `GET` | `/api/v1/run/<run_id>/patch` | `run.patch`, proxied |
| `GET` | `/api/v1/disk` | `server.disk`, proxied |
| `GET` | `/api/v1/capabilities` | what this gateway can do |
| `GET` | `/ws/events` | event subscription (WebSocket) |
| `GET` | `/ws/attach/<run_id>` | PTY attach (WebSocket) |
| `GET` | `/ws/shell` | interactive workspace shell (WebSocket) |
| `POST` | `/local/v1/<verb>` | client-machine verbs (table below) |

An `/api/`, `/ws/`, or `/local/` path hit with the wrong method answers
`405` with a JSON error body instead of falling through to the SPA.
Request bodies are capped at 1 MiB, matching the remote gateway.

### `GET /api/v1/capabilities`

```json
{"gateway":"local","methods":["*"],"ws":["events","attach","shell"],
 "local":["daemon.install","daemon.status","image.scaffold","link.repo",
          "link.status","pull","sync.start","sync.status","sync.stop"]}
```

`methods` is `["*"]` because this gateway forwards every control-channel
method; `ws` adds `shell` to the remote gateway's `events` and `attach`;
`local` is the sorted `/local/v1` verb list. The SPA's `useCapability`
seam reads this to gate the local-only surfaces.

## `/local/v1` verbs

`POST /local/v1/<verb>` with one JSON object as the body (an empty body is
an empty params object). Failures answer the same error envelope and
status mapping as the proxied API; an unknown verb answers `404` with
`-32601`. These verbs run with the user's own filesystem and git
authority.

| Verb | Request | Response |
| --- | --- | --- |
| `link.status` | `{}` | `{"linked":bool,"addr":"...","user":"...","repo":"..."}` |
| `link.repo` | `{"repo":"/path/to/clone","workspace_id":"..."}` (`workspace_id` optional) | `{"repo":"...","remote":"aether","url":"..."}` |
| `pull` | `{"run_id":"..."}` | `{"branch":"...","ref":"...","output":"..."}` |
| `sync.start` | `{"run_id":"...","force":bool}` | `{"run_id":"...","state":"running"}` |
| `sync.stop` | `{"run_id":"..."}` | `{"run_id":"...","state":"stopped"}` |
| `sync.status` | `{}` | `{"sessions":[{"run_id":"...","state":"...","conflict":"..."\|null}]}` |
| `daemon.install` | `{"server":"host:port","repo":"..."}` (`repo` defaults to the linked one) | `{"unit_path":"...","note":"..."}` |
| `daemon.status` | `{}` | `{"installed":bool,"unit_path":"..."}` |
| `image.scaffold` | `{"repo":"...","kind":"dockerfile"\|"devcontainer"}` (`repo` defaults to the linked one) | `{"written":["..."]}` |

- `link.repo` honors a `workspace_id` naming the workspace the remote URL
  must carry (the onboarding wizard sends the one just picked). Without
  it the workspace resolves exactly like `aether link --repo`: a single
  workspace resolves implicitly; none or several answers `-32002`
  (invalid state) and is resolved server-side or with the CLI's
  `--workspace` flag first.
- `pull`, `sync.start`, and `sync.stop` refuse with `-32002` when no repo
  is linked. A sync session's states are `starting` (the overlay is
  dialing the run worktree), `running`, `stopped`, `conflict` (with the
  conflict text in `conflict`), and `error`. A conflict is also reported
  to the server as a `sync.conflict` call so both affected members see
  the event; `sync.stop` dismisses a standing conflict.
- `image.scaffold` refuses with `-32002` when the files already exist,
  rather than overwriting them.

## WebSockets

`/ws/events` and `/ws/attach/<run_id>` speak exactly the framing
documented in [dashboard-api.md](dashboard-api.md) - subscribe header,
`{"ok":...}` ack, text event frames and the `4000` backlog close on
events; binary output and JSON `input`/`resize` control frames on attach.
One difference from the remote gateway: there is no token watch closing
live sockets with `1008`, because the token cannot be revoked - it lives
and dies with the process.

`GET /ws/shell` is local-only: an interactive workspace shell (bootstrap
tools, harness login, agent setup) with the attach socket's frame
protocol - binary output, JSON control frames for input and resize,
always honored.

1. Client sends one **text** frame: a `protocol.WorkspaceShellRequest`
   (`workspace` selector, `mode`, optional `harness`, geometry, and the
   agent-setup fields), within 10 seconds of the socket opening. Missing
   geometry defaults to 80x24.
2. Server answers one **text** frame: a `WorkspaceShellResponse` -
   `{"ok":true,...}` echoing the effective selection and geometry, or
   `{"ok":false,"code":...,"error":"..."}` followed by a close.
3. Binary output frames stream; text control frames go back, as on
   attach. Client frames are capped at 64 KiB.
4. The shell exiting cleanly closes the socket with **1000**; a nonzero
   remote exit status closes with **4001** and the error text as the
   reason, so the SPA can tell a dirty exit from a clean one.
