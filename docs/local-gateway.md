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
(unavailable); a failure the server itself answered passes through untouched as
that `protocol.Error`. Streams get the same treatment with a guard: a channel
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

### `GET /api/v1/capabilities`

```json
{"gateway":"local","methods":["*"],"ws":["events","attach","terminal"],
 "local":["daemon.install","daemon.status","env.harnesses","forward.start",
          "forward.status","forward.stop","git.identity","link.apply","link.repo",
          "link.status","link.switch","pull","pull.switch","repo.fast-forward",
          "repo.push","sync.start","sync.status","sync.stop",
          "update.apply","update.check","update.status"],
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


| Method | Params | Result |
| --- | --- | --- |
| `run.patch` | `RunPatchParams` (`{"run_id":"...","from":"...","to":"..."}`; `from` and `to` optional) | `RunPatchResult` - the same JSON shape the patch `GET` answers |
| `server.disk` | none | `ServerDiskResult` - the same JSON shape the disk `GET` answers |
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
`revision` and may be writable. Binary or truncated content is bounded to a
read-only response with an empty revision. The file/editor limit is 512 KiB;
the same limit applies to `files.write` and `config.write`.

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
rules as `files.write`, while `config.import` installs a one-time directory
selection into that home.
All of the member's run containers and environment terminal mount one shared
read-write persistent HOME. A file edit, configuration import, or manual CLI
profile operation is therefore visible to already-running processes
immediately, although a tool may need to reload its configuration.

The browser uses the selected root's `runtime_ignores` metadata before
reading or uploading any bytes. `runtime_ignores` contains exact,
case-sensitive root-relative paths and component prefixes; trailing slashes
are ignored for matching. These lists are per harness, so a runtime file
ignored for Claude is not implicitly ignored for OMP or a member-defined
custom harness. Known credential names wherever they occur in a path, and
every basename ending in `.pem`, remain filtered by the existing
destination-independent credential policy. The browser keeps raw local file
handles so it can recompute an import when the destination changes, and
cannot change destinations during import or after a result exists.
All remaining bytes are uploaded and server-scanned. Imports allow at most
2,000 files, 1 MiB per file, and 20 MiB decoded in aggregate. Empty and binary
regular files are preserved; a browser import sends mode `0644` and cannot
preserve executable mode or symlinks. Existing remote modes are preserved.
The server rejects unsafe paths, symlink components, hardlinks, and
non-regular destinations.

The result's `files` and `bytes` count
accepted files only; `excluded` reports server-side credential, ignore,
secret, or safety exclusions. Explicit CLI profile `push`, `status`, and
`rollback` remain separate manual operations; the dashboard does not invoke
profile synchronization or watch a local directory.

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

The generic HTTP proxy caps ordinary `/api/v1` JSON bodies at 1 MiB,
`files.write` and `config.write` at 4 MiB, and `config.import` at 30 MiB.
The SSH control-channel line cap is 32 MiB; the decoded import limits above
remain authoritative.

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
- `truncated` reports that the diff outgrew the 512 KiB ceiling; `patch` then
  ends at the last whole line that fit. Read the run branch over git for the
  rest - the dashboard renders diffs, it does not serve repositories.
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

- The same 512 KiB diff ceiling applies to `run.patch`; `truncated` reports
  that the patch ends at the last whole line that fit. `from` and `to` select
  one interval here exactly as they do on the `GET`.
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

`link.apply` saves the link and swaps the gateway connection in place, so
subsequent API and WebSocket requests use the new server without a restart. If
no SSH key is offered and the server requires one, it may create
`~/.ssh/id_ed25519` and its `.pub` file.

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
An attach the gateway refused, one parked on a `session ended` close, and a
run still waiting for its PTY session are not reopened by either event.

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

PTY attach. Output is binary, control is JSON, matching the terminal view's
needs.

1. Client sends one **text** frame with the attach header (the run comes
   from the path). The header must arrive within 10 seconds or the socket
   is closed. The dashboard requests write on first entry; a CLI read-only
   mirror sends `{}`:

   ```json
   {"write":true,"cols":120,"rows":40}
   {"write":true,"follow":true,"cols":80,"rows":24}
   ```

   `resume` asks to reattach without the scrollback replay: the client
   already holds this session's screen and is reattaching only to change
   what it may do, which is what the dashboard does when you take control
   or hand it back. It carries `cursor`, the output count the last ack
   reported plus every live byte received since, and the replay is exactly
   what followed it - usually nothing, and never the whole scrollback. The
   client keeps what is on screen along with the terminal state behind it.
   The ack answers with `"resumed":true`; a session whose ring no longer
   reaches back that far answers `"resumed":false` and replays everything
   instead, which the client has to clear its screen for.

   `follow` says the client renders the session at the size it already is
   and imposes none of its own, so it is left out of the minimum the PTY is
   sized to whether or not it can write (step 4). Its `cols` and `rows` are
   then only what a session with no PTY of its own is laid out at - a
   finished run's replay. The dashboard follows from a phone, mirroring and
   steering alike, which is how a 45-column screen steers an agent without
   reflowing that agent's screen for everyone else watching it.

2. Server answers one **text** frame: `{"ok":true,"cols":120,"rows":40,"replay":4096}`,
   or `{"ok":false,"code":-32001,"error":"..."}` followed by a close. The
   ack's geometry is the session's live PTY size, not an echo of the header,
   and falls back to the header only when there is no session to have one.
   The optional `replay` value is the number of binary scrollback bytes
   that follow the ack before live output; clients should mute
   terminal-generated replies until those bytes have been parsed. It leads
   with the terminal modes the session is in - bracketed paste, cursor,
   autowrap, mouse reporting - rebuilt rather than recorded, since the
   bytes that set them left the scrollback long ago. `cursor` is how much
   of the session's output the client holds once that replay is parsed;
   it is what a later `resume` sends back.
   A write attach is refused with `-32001`
   unless the member holds the **steer** capability on that run; dropping
   `"write"` always works for a member who can see the run. An unknown run is
   refused with `-32000`.
   A finished run attaches as a read-only replay of its recorded
   transcript, ending with the session-end close below. A `queued`,
   `provisioning` or `running` run with no session is refused with `-32004`
   rather than held open - the container is still being built, or recovery is
   starting the session - as is a finished run whose transcript predates
   recording. The refusal is the answer, so a client that means to wait for a
   session has to retry rather than expect the socket to stay open.
3. Server then streams terminal output as **binary** frames.
4. Client sends **text** control frames:

   ```json
   {"type":"input","data":"ls -la\r"}
   {"type":"resize","cols":132,"rows":50}
   ```

   Input from a read-only attach is ignored; its resizes are not, because
   whether they count is the session's to decide. The shared terminal
   geometry is the per-dimension minimum over the attaches that impose one:
   every write-capable attach, plus a read-only one while it is the only
   attach that is not a `follow` client. So a narrow writer reflows the
   agent's screen for everyone, a follower never does, and a lone watcher
   sizes the PTY to its own window the way `ssh` does - until a second
   attach arrives, when it stops imposing and the minimum is recomputed
   without it.
5. Server sends one **text** control frame to attached dashboard clients
   whenever the runtime accepts a changed PTY size:

   ```json
   {"type":"geometry","cols":132,"rows":43}
   ```

   Every dashboard terminal renders at this size, including writers: another
   writer can make the effective grid smaller than the local pane. The pane's
   requested geometry remains separate from the rendered grid. This is a
   relayed SSH `window-change` request, not a persisted event; a reattach learns
   the current size from the ack.

   A fresh screen-bearing attach requests a same-size redraw nudge even when
   it follows another viewer. A successfully resumed attach keeps its screen
   and does not request that nudge. Screenless adapter taps request neither
   a size nor a redraw.

   Client frames are capped at 64 KiB; the SPA splits larger input (a paste)
   across several ordered `input` frames.
6. The server re-checks the attach's authorization every few seconds. A
   write attach whose member loses **steer** (role change, handoff, run
   protection, workspace policy) closes with **1008**, reason
   `steer permission withdrawn`; the SPA reconnects as a read-only mirror.
   A member removed or set back to pending closes with **1008**, reason
   `membership withdrawn`, and the SPA stops reconnecting. The run's
   terminal session ending - the run shell exiting, or a finished run's replay
   draining - closes with **1000**, reason `session ended`, and the SPA
   stops reconnecting; any other end closes with **1011**.

Closing the socket detaches; the run is unaffected.

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
2. The gateway answers `{"ok":true,"tab":"main","cols":120,"rows":40,"replay":4096}`
   with the session's live geometry
   or a JSON error followed by a close. When present, `replay` is the number
   of binary scrollback bytes that follow the ack before live output; clients
   should mute terminal-generated replies until those bytes have been parsed.
   At most six tabs may be active.
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
