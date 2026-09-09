# The local gateway (`aether gui`)

`aether gui` serves the dashboard from the user's own machine
(`internal/localgw`): the embedded SPA, the `/api/v1` shape, and the
WebSocket surfaces, all proxied over the machine's SSH connection to the
linked server. It is the only web transport Aether ships; the server itself
listens on SSH and nothing else. Because the identity is the member's own
SSH key rather than a token minted somewhere else, the **full
control-channel method map** is reachable - no allowlist - plus the
client-machine verbs under `/local/v1` that only a machine with the user's
repository and SSH key can offer. The security stances are in
[security.md](security.md#the-dashboard-gateway); the SPA that runs against
it is in [dashboard-frontend.md](dashboard-frontend.md).

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

### Agent OAuth logins

When an agent prints an OAuth URL in the dashboard, click it. If the URL
contains an HTTP loopback callback, the dashboard opens a blank browser tab,
starts the local forward, and only then loads the authorization page. The
forward targets the run or environment terminal where the link appeared.

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

The gateway holds no server code: every read and write is a
control-channel call proxied over one SSH connection to the linked server,
through the same `internal/cli` client the terminal commands use. One
fresh subsystem channel per WebSocket for events, attach, terminal, and sync -
so the HTTP handlers never know they are riding SSH.

The connection is dialed lazily on first use and shared. When a call fails
on transport (a server restart, a dropped network) the backend redials
once and retries once before surfacing `-32004` (unavailable); a failure
the server itself answered passes through untouched as that
`protocol.Error`. Streams get the same treatment with a guard: a channel
that fails to open triggers a redial only when a keepalive shows the
connection is actually gone, because tearing down a healthy connection
would kill every live stream riding on it.

Every `-32004` carries a message prefix that says who has to fix it, and
both map to HTTP 503 as before. `network unreachable: ` means this
machine could not even attempt the connection (DNS resolution failed, or
the kernel reported no route or an interface down), so the user fixes
their own connectivity. `server unreachable: ` is everything else and is
the default: a refused connection, a dial timeout, a failed SSH
handshake, or a wedged call, where the server is the thing to check. The
split stops at unambiguous cases on purpose, since a refusal or a timeout
cannot tell a stopped server from a firewall, and a wrong guess sends the
user to fix the wrong thing. Dial failures are classified inside the
shared dial path, so the `/ws/events` refusal frame carries the same code
and prefix as a `POST /api/v1` error.

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
| `GET` | `/ws/attach/<run_id>?shell=<tab>` | writable run-container shell tab (WebSocket) |
| `GET` | `/ws/terminal?tab=<tab>` | persistent member environment terminal (WebSocket) |
| `GET` | `/ws/envscan` | environment scan on this machine (WebSocket) |
| `POST` | `/local/v1/<verb>` | client-machine verbs (table below) |

Anything that is not `/api/`, `/ws/`, or `/local/` is served from the
embedded `web/dist`, without a token - the SPA bundle is not secret and has
to load before it can present one. Unknown paths fall back to `index.html`
so client-side routing works on a hard refresh. An `/api/`, `/ws/`, or
`/local/` path hit with the wrong method is the exception: it answers `405`
with a JSON error body instead of falling through to the SPA, so a
wrong-verb client bug cannot masquerade as a `200`. Request bodies are
capped at 1 MiB.

### `POST /api/v1/<method>`

The path segment after `/api/v1/` is the JSON-RPC method name, dots
included: `POST /api/v1/run.list`. The request body is the method's
`params` object (an empty body means no params). Success is `200` with the
method's result object as the whole body; failure is a non-2xx status with
the JSON-RPC error object wrapped:

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

Param and result shapes are the ones in `internal/protocol` (`wire.go` and
the per-feature files), unchanged by this transport, and every call passes
the same capability checks the SSH transport applies.

`run.delete` uses the same `Kill` capability as `run.kill` and accepts the
same `{"run_id":"..."}` params. For a live run it stops the container and
waits for supervision to publish the final branch before removing the
checkout, transcripts and run-owned database records. For an old run it
removes the checkout and transcripts directly. The run's timeline remains as
audit history.

### `GET /api/v1/capabilities`

```json
{"gateway":"local","methods":["*"],"ws":["events","attach","terminal","envscan"],
 "local":["daemon.install","daemon.status","env.harnesses","forward.start",
          "forward.status","forward.stop","git.identity","link.apply","link.repo",
          "link.status","link.switch","profile.preview","profile.push","pull",
          "pull.switch","repo.fast-forward","repo.push","repo.sync","sync.start",
          "sync.status","sync.stop","update.apply","update.check","update.status"],
 "version":"v1.2.3","commit":"abc1234"}
```

`methods` is `["*"]` because this gateway forwards every control-channel
method; `ws` lists the WebSocket surfaces it serves; `local` is the sorted
`/local/v1` verb list. A client probes this rather than hard-coding what it
is talking to; the SPA's `useCapability` seam reads it to gate the
local-only surfaces.

`version` and `commit` are the `aether` build serving this gateway, which is
the only way the SPA can learn what CLI it is running against - `server.info`
answers for the server. Both are absent on a gateway that predates them.

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

### Control-channel methods this gateway calls

The two `GET` endpoints above are backed by SSH control-channel methods, as
are the file reads and the member and workspace writes below. This gateway
proxies the whole API shape over SSH, so it needs all of them without a
listener on the server.

| Method | Params | Result |
| --- | --- | --- |
| `run.patch` | `RunPatchParams` (`{"run_id":"...","from":"...","to":"..."}`; `from` and `to` optional) | `RunPatchResult` - the same JSON shape the patch `GET` answers |
| `server.disk` | none | `ServerDiskResult` - the same JSON shape the disk `GET` answers |
| `files.tree` | `FilesTreeParams` (`{"workspace_id":"...","run_id":"...","path":"src"}`; `run_id` optional) | `FilesTreeResult` - immediate file and directory entries |
| `files.read` | `FilesReadParams` (`{"workspace_id":"...","run_id":"...","path":"README.md"}`; `run_id` optional) | `FilesReadResult` - read-only content, size, binary, and truncation |
| `files.diff` | `FilesDiffParams` (`{"run_id":"...","path":"README.md"}`) | `FilesDiffResult` - one file's patch against the run base |
| `terminal.status` | none | `TerminalStatusResult` - whether the member environment is running, its `image`, optional `saved_image`, start time, and active tabs |
| `terminal.stop` | none | empty result; stops the member environment and its tabs |
| `env.save` | none | `EnvSaveResult` (`{"image":"aether/member-<id>:<unix-seconds>"}`) - commits the running environment terminal as the member's image |
| `env.reset` | none | empty result; stops the environment, forgets and removes the saved image |
| `workspace.origin` | `WorkspaceOriginParams` (`{"workspace_id":"...","origin":"https://github.com/acme/app.git"}`; `origin` empty clears it) | `WorkspaceOriginResult` - the workspace with its new `origin`, the upstream every new run checkout's `origin` remote points at |
| `github.connect` | none | `GitHubConnectResult` (`{"login":"...","signing_key":"ssh-ed25519 ...","fingerprint":"SHA256:..."}`) - finishes the GitHub connection for the calling member |

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
- `github.connect` is member-scoped and takes no parameters: it acts on the
  calling member's own environment terminal. It runs `gh` inside that
  container, so it answers `-32002` (invalid state) when the terminal is not
  running, and again when gh is not logged in to `github.com` there - that
  second message ends with gh's own reason for the account, such as `HTTP
  401: Bad credentials`, so the dashboard and the CLI can show what gh said
  rather than a summary. `signing_key` is the public key line and
  `fingerprint` its `SHA256:` fingerprint; the private key never leaves the
  server. The dashboard's Agents step calls this after the member
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
| `link.status` | `{}` | `{"server_configured":bool,"linked":bool,"addr":"...","user":"...","repo":"...","links":[{"name":"...","addr":"..."}],"active":"..."}` (`links`/`active` present only with named profiles; `server_configured` reports a configured server even when no repository is linked) |
| `link.switch` | `{"name":"..."}` | always `-32002` (invalid state): `restart aether gui --server <name> to switch servers` |
| `link.repo` | `{"repo":"/path/to/clone","workspace_id":"..."}` (`workspace_id` optional) | `{"repo":"...","remote":"aether","url":"...","origin":"..."}` (`origin` is the workspace's upstream afterwards, omitted when it has none) |
| `git.identity` | `{}` | `{"name":"Ada Lovelace","email":"ada@example.com"}` - this machine's `git config user.name` and `user.email`; either is empty when unset |
| `profile.preview` | `{"harness":"claude"}` | the whole preview object (below) |
| `profile.push` | `{"harness":"claude"}` | `{"harness":"...","snapshot_id":"...","digest":"...","files":42,"bytes":183422,"skipped":[...]}` |
| `pull` | `{"run_id":"..."}` | `{"branch":"...","ref":"...","output":"...","current":bool,"dirty":bool}` |
| `pull.switch` | `{"run_id":"..."}` | `{"branch":"..."}` |
| `repo.push` | `{"workspace_id":"..."}` (optional) | `{"branch":"...","remote":"aether","state":"pushed"\|"up-to-date"\|"behind"\|"diverged","local_commit":"...","workspace_commit":"...","ahead":0,"behind":0,"output":"..."}` |
| `repo.fast-forward` | `{"workspace_id":"..."}` (optional) | `{"branch":"...","commit":"...","current":bool,"dirty":bool,"output":"..."}` |
| `repo.sync` | `{"workspace_id":"..."}` (optional) | `{"branch":"...","output":"..."}` |
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
- The branch keeps whatever upstream it already had. A member's `<base>`
  usually tracks their own remote, and a catch-up that repointed it at the
  workspace would redirect their next `git pull`; only run branches
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
- `repo.sync` fetches `<base>` with
  `git fetch --no-tags origin <base>`, then pushes
  `refs/remotes/origin/<base>:refs/heads/<base>` to `aether` without force.
  `<base>` is the selected workspace's base branch, and `output` joins the
  trimmed stdout and stderr from both commands. It leaves the local branch and
  working tree alone.
- `repo.sync` refuses with `-32002` (invalid state) when there is no linked
  repository, no `origin` remote, no `aether` remote, or the `aether` remote
  points at another workspace. A failed fetch or push answers `-32603`
  carrying git's own output, including non-fast-forward refusals.
- Every git command that dials a remote is bounded at ten minutes and runs
  with `GIT_TERMINAL_PROMPT=0`, so git cannot block on its own credential
  prompt. The bound covers the compare and the push separately, so a
  `repo.push` that compares and then pushes can take twenty minutes;
  `repo.fast-forward` bounds its compare at ten and finishes locally, and
  `repo.sync` bounds its fetch and push together at ten. The bound does not
  reach `ssh`: a passphrase-protected key with no agent still waits on
  ssh's own prompt until the ten minutes are up. Load the key into an agent
  before pushing, fast-forwarding, or syncing from the dashboard.
- `profile.preview` runs the discovery `aether profile push --agent
  <harness>` would run and uploads nothing. It reports what a push would
  carry, grouped into categories a developer recognizes, and everything
  the guards left behind:

  ```json
  {"harness":"claude","root":"/home/you/.claude","present":true,
   "files":42,"bytes":183422,
   "categories":[{"category":"skills","files":12,"bytes":40201,
                  "paths":["skills/pdf/SKILL.md"],"truncated":false}],
   "excluded":[{"path":"notes/key.txt","reason":"secret",
                "detail":"secret detected (aws-access-token) at 3:9"},
               {"path":".credentials.json","reason":"credential",
                "detail":"credential file excluded for claude"}],
   "excluded_total":2}
  ```

  Categories, in the order they are reported: `memory` (standing
  instructions - `CLAUDE.md`, `AGENTS.md`, `memory/`), `skills`,
  `commands` (`commands/`, and codex's `prompts/`), `settings`, `mcp`,
  `plugins`, `other`. `paths` is capped at 200 entries per category, with
  `truncated` set when it was cut; `files` and `bytes` stay exact.
  `reason` on an exclusion is `credential` (a denylisted basename),
  `secret` (a content-scanner finding in a file the user wrote),
  `vendored-secret` (a finding inside a plugin tree the harness installs
  into - claude's `plugins/cache/` and `plugins/marketplaces/`), `ignored` (an
  `.aether-profile-ignore` match, or one of the per-harness defaults in
  [harnesses.md](harnesses.md)), `symlink` (a link out of the profile
  root, skipped rather than followed - its target is never opened),
  `not-regular` (a socket, named pipe, or device node, refused on its
  mode without being opened), `too-large` (over the 1 MiB a push allows
  for one file), or `over-budget` (the 20 MiB a snapshot holds was
  already filled).
- An ignored directory is reported once, as the directory, rather than
  once per file inside it. `excluded` is capped at 200 entries;
  `excluded_total` is the exact count.
- `excluded` lists every `secret` first, then the rest in path order.
  Those are the entries a caller has to put in front of the user, and
  ordering them by path would let a profile with a few hundred ignored
  files push the one file the user has to act on past the cap. It also
  means a capped list still carries all of them, and so an exact count,
  unless every entry sent is a `secret`.
- The snapshot budget is spent by category priority - memory, skills,
  commands, settings, mcp, plugins, other - not directory order, so the
  files this feature exists to carry are not crowded out by whatever
  sorts first.
- The two size reasons are decided from the file's stat, before it is
  opened, so an oversized file is never read and never scanned. That is
  not only a saving: an agent's configuration directory routinely holds
  hundreds of megabytes of transcripts, and scanning those would make a
  preview take minutes. The caps are the server's own
  (`internal/profile`), so the preview offers exactly the files a push
  can carry.
- No exclusion refuses a gateway push. Every one of them - both secret reasons,
  symlink escapes, and both size caps - lets the push succeed carrying
  what is left, so `excluded` and `excluded_total` are the whole preview
  and there is no field a caller has to check before offering the import.
  The two secret reasons differ only in what a caller can offer the user:
  a `secret` is in a file the user wrote and can edit, a
  `vendored-secret` is a string in a package the harness installed. Both
  carry the scanner's rule and location in `detail`.
- `present:false` - this machine has no profile root for that harness -
  is a normal answer with zero counts, not an error. A harness name the
  registry does not know, or one with no profile sync, answers `-32602`.
- `profile.push` performs the push `aether profile push --agent
  <harness>` performs, through the gateway's SSH connection: the same
  discovery, the same per-harness credential denylist, the same secret
  scanner, and the same content-addressed delta against the server's
  current head. **It takes no allow-secret parameter.** A scanner finding
  drops the one file it named and the push runs; carrying a flagged file
  anyway stays on the CLI, where `--workspace` makes it attributable on a
  timeline. A missing profile root refuses with `-32002`. `skipped`
  carries every exclusion the walk made - both secret reasons, the size
  caps, and symlink escapes - in the same shape `profile.preview` uses:
  the push succeeded without those files, so this is the only place the
  caller learns they are not on the server.
- Both verbs walk the whole profile root, and both stop when the request
  is cancelled: a client that closes the connection stops the work on
  this machine, rather than only stopping its own wait.
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
same origin as the API. Every handshake carries the token, as
`Authorization: Bearer` or `?token=` - browsers cannot set headers on a
WebSocket handshake. There is no token watch closing live sockets, because
the token cannot be revoked: it lives and dies with the process.

### `GET /ws/events`

Same subscription semantics as the SSH events subsystem, so a client that
lost its socket resumes without gaps.

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
with `"replay":true,"after_seq":<last seq>`. When the SSH event stream ends
for any reason - a dropped connection, a server restart, or a per-client
buffer overflow - the socket closes with code **1012** (service restart),
reason `event stream ended; resubscribe with after_seq`. That close is the
signal to resubscribe from your last `seq`, not an error to surface; the
replay recovers anything dropped.

### `GET /ws/attach/<run_id>`

PTY attach. Output is binary, control is JSON, matching the terminal view's
needs.

1. Client sends one **text** frame with the attach header (the run comes
   from the path). The header must arrive within 10 seconds or the socket
   is closed. The dashboard requests write on first entry; a CLI read-only
   mirror sends `{}`:

   ```json
   {"write":true,"cols":120,"rows":40}
   ```

2. Server answers one **text** frame: `{"ok":true,"cols":120,"rows":40,"replay":4096}`,
   or `{"ok":false,"code":-32001,"error":"..."}` followed by a close. The
   optional `replay` value is the number of binary scrollback bytes that follow
   the ack before live output; clients should mute terminal-generated replies
   until those bytes have been parsed. A write attach is refused with `-32001`
   unless the member holds the **steer** capability on that run; dropping
   `"write"` always works for a member who can see the run. An unknown run is
   refused with `-32000`.
   A finished run attaches as a read-only replay of its recorded
   transcript, ending with the session-end close below; only a run whose
   session is transiently missing (server recovery in progress) or whose
   transcript predates recording is refused with `-32004`.
3. Server then streams terminal output as **binary** frames.
4. Client sends **text** control frames:

   ```json
   {"type":"input","data":"ls -la\r"}
   {"type":"resize","cols":132,"rows":50}
   ```

   Control frames from a read-only attach are ignored. Only write-capable
   attaches affect the shared terminal geometry. Client frames are capped at
   64 KiB; the SPA splits larger input (a paste) across several ordered
   `input` frames.
5. The server re-checks the attach's authorization every few seconds. A
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

1. Client sends one text header with `cols` and `rows`. The gateway ensures the
   member's environment container and the requested shell.
2. The gateway answers `{"ok":true,"tab":"main","cols":120,"rows":40,"replay":4096}`
   or a JSON error followed by a close. When present, `replay` is the number
   of binary scrollback bytes that follow the ack before live output; clients
   should mute terminal-generated replies until those bytes have been parsed.
   At most six tabs may be active.
3. Output is binary. Input and resize are text frames:

   ```json
   {"type":"input","data":"ls -la\r"}
   {"type":"resize","cols":132,"rows":50}
   ```

Closing the socket detaches without stopping the shell. A normal shell exit
closes with **1000**. Membership loss closes with **1008**. `terminal.status`
reports the running container's `image`, its `saved_image` when set, start
time, and active tabs; `terminal.stop` stops the container and deletes its tab
sessions while preserving the member home.


### `GET /ws/envscan`

Runs the onboarding **profile** scan on this machine. The chosen setup-capable
coding agent runs headless and recommends which local agent configurations are
worth importing into Aether. Every frame is JSON text. Before the agent runs,
the gateway widens its `PATH` from your login shell the way `env.harnesses`
does.

1. The client sends one **text** start frame within 10 seconds. `mode` must be
   `profile`; `harness` names the setup-capable agent to run. `repo_path` is
   optional. When present, it must name a Git repository; the agent runs from
   that directory, never writes it, and the scan fails if its Git status
   changes:

   ```json
   {"harness":"claude","mode":"profile"}
   {"harness":"claude","mode":"profile","repo_path":"/path/to/clone"}
   ```

   The gateway answers an unsupported mode with one `error` frame and a
   **1008** close. A bad `repo_path` answers one `error` frame naming the
   problem, then closes with **1000**.

2. Server streams progress frames while the agent runs:

   ```json
   {"type":"status","status":"running"}
   {"type":"output","line":"one raw line of agent output"}
   ```

   Statuses arrive in order: `detecting`, `running`, `validating`, and
   `retrying` when the agent's output failed validation and the one automatic
   retry starts.

3. Exactly one terminal frame ends the scan, then the socket closes with
   **1000**. Success carries a recommendation with one entry per harness the
   agent was shown:

   ```json
   {"type":"result","recommendation":{"harnesses":[
     {"harness":"claude","import":true,"categories":["skills","commands"],
      "reason":"..."},
     {"harness":"codex","import":false,"categories":[],"reason":"..."}]}}
   ```

   The recommendation is a proposal, never an action. The user edits and
   approves it before importing with a separate `profile.push` call per
   harness. The agent sees names, paths, category counts, and sizes only:
   file contents, credential paths, and anything the denylist or secret
   scanner flagged never reach the prompt. A machine with no agent
   configuration answers one `error` frame:
   `no agent configuration found on this machine; nothing to import`.

   Failure carries the reason and the last agent output for diagnosis:

   ```json
   {"type":"error","detail":"the scan timed out after 10m0s","output_tail":"..."}
   ```

One scan runs at a time per gateway. A second start frame answers an `error`
frame whose `detail` says a scan is already running, then closes with **1008**.
Closing the socket cancels the scan and kills the agent process; its scratch
directory is removed in every outcome.
