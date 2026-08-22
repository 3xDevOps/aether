# Full-Capability GUI and Desktop App Implementation Plan

**Goal:** Grow the dashboard from a monitor into a client that can do
everything the CLI can - launch, administer, onboard agents, and land
results - and package it as a desktop application, without weakening the
remote gateway's security posture.

**Architecture:** A new client-side component (working name `aether gui`)
that dials the server over SSH with `internal/cli` - the same key
discovery, ssh-agent, known_hosts, and tailnet identity every CLI command
uses - and serves the existing SPA on a tokened loopback port. This
**local gateway** speaks the same HTTP/WS API shape as `internal/dashboard`
but proxies to the SSH control channel, so it carries the full RPC method
map plus two things the remote gateway cannot have: workspace-shell
streams and local-machine verbs (git, sync overlay, daemon). The Electron
shell arrives last, as a thin wrapper around the gateway sidecar; all
privileged logic stays in Go.

```
User's machine                                  Server box
+--------------------------------------+
|  SPA (same bundle)                   |
|    | HTTP/WS, local token            |
|  aether gui (Go local gateway)  -----+--SSH--> aether-server
|    | git          | mutagen          |            ^
|  local clone    sync overlay         |            |
+--------------------------------------+            |
                                                    |
Browser tab via `aether dash` --HTTP--> remote gateway (allowlist subset)
```

**Why not widen the remote gateway instead:** the HTTP allowlist exists
because a bearer token travels in a URL and must never be a route to a
durable credential (`docs/security.md`). And no allowlist change can give
a browser tab a filesystem, a git binary, or an ssh-agent, so the local
verbs (`link`, `pull`, `sync`, `daemon`, `image`) are unreachable from a
remote-served page regardless. The capability gap is an identity and
locality gap; a process holding the user's SSH identity on the user's
machine is the only architecture that closes it. The remote gateway and
its allowlist do not change.

**Why Electron over a Go-native shell (Wails):** binding Go methods
straight into a webview would fork `src/lib/api.ts` into a second data
layer and let the browser-served dashboard diverge from the desktop one.
Keeping one SPA against one API shape, reached through two gateways, is
the maintainability line. If distributable size ever matters more than
shell features, a Wails window can host the same loopback gateway
unchanged; that hybrid is the fallback, not Wails bindings.

**Non-goals:**

- No change to the remote gateway's method allowlist, token model, or
  re-authorization behavior. Browser-via-`aether-dash` remains the
  zero-install read/steer path and gains nothing credential-shaped.
- No second web framework, no second state store, no URL router. New
  surfaces arrive through the three extension seams the SPA already has
  (route registry, store slices, slots - `docs/dashboard-frontend.md`).
- No merge automation. The review-and-land surface fetches and closes;
  Aether never merges anything for you.
- No Node privileges in the Electron renderer. The page talks only to the
  sidecar's tokened loopback HTTP; SSH keys never leave the Go process.
- No multi-server support in the first desktop release; the CLI config is
  single-link today and the app follows it (Phase 4 revisits).

## Capability inventory

Every CLI capability, sorted by why the browser dashboard lacks it:

| Category | Capabilities | Gap |
| --- | --- | --- |
| Already in the SPA | board, terminal mirror/steer, diff, events, approvals, presence, cost readout, `run.launch/inject/kill/pause/resume/close/handoff`, `template.launch` | none |
| SSH-identity RPC | `session.new/settings`, `workspace.add`, `workspace.tools.*`, `member.invite/approve/remove/color`, `budget.set`, `template.save/delete`, `schedule.*`, `profile.*`, `agent.register/list`, `run.protect`, `run.relaunch`, `run.pull` | off the HTTP allowlist by design |
| Interactive PTY beyond run attach | `agent add`, `setup`, `workspace bootstrap` (the three workspace-shell modes) | remote gateway has `/ws/attach/<run>` only |
| Local machine verbs | `link` (config + git remote), `pull` (git fetch), `sync` (mutagen overlay), `daemon` (install/run), `image` (scaffold files), `update` | a browser tab has no filesystem, git, or ssh-agent |

The local gateway closes rows 2-4 by construction: it *is* an SSH client.

## Design

### The local gateway (`aether gui`)

A new `internal/localgw` package and an `aether gui` CLI command.

- **Transport reuse, not reimplementation.** The gateway serves the same
  route shapes as `internal/dashboard` - `POST /api/v1/<method>`,
  `GET /ws/events`, `GET /ws/attach/<run_id>`, the diff patch read - but
  each handler forwards to the SSH control channel through
  `internal/cli`'s `Conn`/`Client`. Param and result shapes stay
  `internal/protocol`'s, unchanged. Where handler logic in
  `internal/dashboard` is transport-independent (WS framing, the
  status-code mapping, header-frame handshakes), extract and share it
  rather than copying.
- **Full method map.** No allowlist: the SSH identity behind the proxy is
  the same one the CLI would use, so every control-channel method the
  member may call is reachable. Capability checks stay server-side, as
  they are for the CLI.
- **Loopback + token, always.** Any local process can reach a loopback
  port (`docs/security.md`), so the gateway binds 127.0.0.1 only, refuses
  any other bind address, and mints one random token per process that the
  opened URL carries exactly as `aether dash` does. The SPA's existing
  token handling (`?token=` into session storage, bearer on HTTP, query on
  WS) works unmodified.
- **New route: `GET /ws/shell`.** Bridges a workspace-shell stream
  (`protocol.WorkspaceShellRequest`: bootstrap, harness-login, or
  agent-setup mode) to a WebSocket with the same framing as
  `/ws/attach/<run>` - one text header frame, binary output, JSON
  input/resize control frames. This is what puts `agent add`, `setup`,
  and `workspace bootstrap` behind an xterm.js pane.
- **New routes: `POST /local/v1/<verb>`.** The local verbs, each a thin
  wrapper over code the CLI already has: `link.status`, `link.repo`
  (add the `aether` git remote), `pull` (`run.pull` RPC then `git fetch`,
  as `cmd/aether/pull.go`), `sync.start`/`sync.stop`/`sync.status`
  (`internal/overlay`), `daemon.install`/`daemon.status`
  (`internal/syncd`), `image.scaffold`. Verbs that shell out to git
  stream their stdout/stderr back in the response so the UI can show the
  real output.
- **Capability descriptor.** `GET /api/v1/capabilities` reports which
  method groups, WS routes, and `/local/v1` verbs this gateway offers.
  The remote gateway gains the same endpoint reporting its allowlist.
  The SPA gates surfaces on this data - never on "am I desktop"
  conditionals.
- **Lifecycle.** `aether gui` holds the SSH connection open for the
  process lifetime, reconnects with backoff, and exposes connection state
  on the events WS so the SPA's existing unreachable-panes handling
  renders it. Exit closes the SSH connection; the loopback token dies
  with the process.

### SPA growth

All through the existing seams; the shell, `sync.ts` lifecycle, and
`lib/api.ts`'s single-module rule are untouched in structure:

- `lib/api.ts` gains the new methods and the `/local/v1` calls, and loads
  the capability descriptor at connect. Views and palette verbs render
  only what the descriptor advertises, so the same bundle serves both
  gateways.
- New store slices (`admin`, `agents`, `shell`, `local`) and registry
  routes for the new surfaces below. Server refusals are rendered
  verbatim, never predicted - the SPA's standing pattern.
- The shell pane reuses the terminal view's attach client structure
  (`src/routes/terminal/attach.ts`): same backoff, same 64 KiB input
  splitting, same header-frame handshake, pointed at `/ws/shell`.

### New surfaces, in end-user terms

- **Onboarding wizard** (first run of the desktop app): server address ->
  key/tailnet detection -> link -> workspace init -> repo picker that adds
  the git remote and pushes. Today's quickstart steps 3-4 - the most
  error-prone stretch of the CLI journey - become three clicks. A
  "prove the plumbing" option drives the shipped `fake` harness end to
  end so a new user sees a real run and a real branch before any vendor
  login.
- **Agents**: `agent add` as a wizard - name, argv templates prefilled
  with the CLI's defaults, then an embedded terminal running the
  agent-setup shell for the vendor login, then the server's registration
  summary. The shell mode from the agent-onboarding plan is reused as-is.
  The pane must make "exit cleanly" unmistakable (a visible Done control
  that sends the exit), and surface `--resume`/`--reset` when a previous
  shell was abandoned, or users will close the window and lose the
  snapshot.
- **Workspaces**: add, image policy display, tools list/verify/rollback,
  bootstrap behind the same shell pane.
- **Members**: roster with invite (one-time code with expiry and a copy
  button), approve pending, remove, color picker.
- **Sessions**: new session, settings (steer policy and the rest).
- **Templates and schedules**: save/delete forms, cron editor with
  next-fire preview from `schedule.list`.
- **Budgets**: the status bar's gauge gains a set-cap affordance. The
  frontend doc's "budgets are set over SSH" holds - this *is* SSH.
- **Profiles**: status and rollback views; push driven by the local
  discovery code in `internal/cli/profile`.
- **Review and land** on run detail: the diff tab grows "Pull branch"
  (`run.pull` + local `git fetch`), copyable `git diff main...<ref>`
  commands, then "Close as merged / abandoned". This closes the loop the
  dashboard currently dead-ends at: today you review in the app and must
  switch to a terminal to land anything.
- **Sync overlay** toggle on run detail; **daemon** setup card in
  settings.

### Desktop shell (Electron)

A thin wrapper, last: it spawns `aether gui` as a sidecar, loads the SPA
from the sidecar's tokened loopback URL, and adds only what a shell can:

- **Native notifications and a tray/dock badge driven by the event
  stream.** Runs parking in needs-attention and approvals queueing are
  the product moments; today someone must keep a tab open and keep
  looking. The badge count is the attention bucket size the board
  selectors already compute.
- Deep links (`aether://run/<id>` -> `navigate('run', {runId})`),
  auto-update (Electron updater for the shell; the sidecar updates
  through `internal/selfupdate` like every other binary), signed builds
  wired into the existing release scripts.
- Security posture: `contextIsolation` on, `nodeIntegration` off, no
  preload-exposed privileges beyond opening external links. The renderer
  reaches the sidecar's loopback HTTP and nothing else. A compromised
  page gets a process-lifetime loopback token, not an SSH key.

### Server-side enablers

Two gaps `docs/dashboard-frontend.md` already tracks as "waiting on the
wire" become glaring in a first-class app and are fixed first:

1. **The needs-attention reason survives only in live events.** Persist
   the reason on the run and carry it on `protocol.Run`, so a card loaded
   after the transition can tell a stall from a clean exit waiting on
   `run.close`. Domain field + store migration + wire change shared with
   the CLI.
2. **The paused state has no hydration source.** Expose it on the run
   read (the scheduler sidecar knows), so the paused badge and the
   palette's pause/resume verbs survive a reload.

Plus the capability descriptor endpoint on both gateways (above).

## Phases and tasks

Each task: test first, minimal implementation, run the package tests,
commit. Later phases depend on earlier ones; tasks within a phase are
mostly independent.

### Phase 0: server-side enablers

1. **Run reason persistence.** Modify `internal/domain` (field),
   `internal/store` (migration + row), `internal/protocol` (wire),
   scheduler write path; SPA reads it in `runState` and drops the
   live-event-only fallback. Test: a run fetched after its transition
   carries the reason; board test asserts the reason line without a live
   event.
2. **Paused-state hydration.** Expose paused on `run.get`/`run.list`;
   SPA seeds `pausedRuns` at hydration; `pausedFromTimeline` remains for
   live updates. Test: reload shows the badge; palette offers the right
   verb immediately.
3. **Capability descriptor.** `GET /api/v1/capabilities` on
   `internal/dashboard` reporting the allowlist and WS routes; SPA
   fetches it at connect and stores it. Test: gateway test asserts the
   descriptor matches the allowlist table; SPA test gates a view on it.

### Phase 1: the local gateway

4. **Package skeleton.** `internal/localgw`: loopback listener, token
   mint/check, static serving of the embedded SPA, `/api/v1/<method>`
   proxy over an `internal/cli` connection, the status-code mapping
   shared with `internal/dashboard`. Test: httptest against a stub
   control channel - token required, method round-trip, error mapping.
5. **WS bridges.** `/ws/events` (subscribe/replay semantics preserved
   end to end) and `/ws/attach/<run_id>` forwarded over SSH streams.
   Reuse/extract the framing from `internal/dashboard` rather than
   copying. Test: stub-socket tests mirroring `ws_test.go`.
6. **`/ws/shell`.** Workspace-shell bridge with attach-style framing,
   all three modes. Test: fake runtime drives a bootstrap shell through
   the WS and sees the banner and clean-exit promotion.
7. **`/local/v1` verbs.** link status/repo, pull, sync start/stop/status,
   daemon install/status, image scaffold - each delegating to the code
   the CLI command uses today (refactor shared logic out of `cmd/aether`
   into internal packages where needed; the CLI commands keep working).
   Test: pull against a scratch git repo; sync against the overlay fake;
   others unit-tested on argument handling.
8. **`aether gui` command.** Flags, browser open, reconnect loop,
   connection state on the events WS, clean shutdown. Exit criterion for
   the phase: every existing dashboard flow works identically through
   `aether gui`, and `session.new` works from the palette.

### Phase 2: the missing surfaces

9. **Capability gating in the SPA.** `lib/api.ts` descriptor load; a
   `useCapability` hook; palette verbs and routes register with a
   capability key. Test: same bundle against remote-gateway and
   local-gateway descriptors shows/hides correctly.
10. **Admin surfaces.** Members (invite/approve/remove/color), sessions
    (new/settings), workspaces (add, tools list/verify/rollback),
    budgets (set), templates and schedules (save/delete, cron editor).
    One slice + route per surface through the registry. Test: rendered
    against the stub API; refusals rendered verbatim.
11. **Shell pane + agents wizard.** Shared xterm shell component over
    `/ws/shell`; `agent add` wizard (prefilled argv, embedded login,
    registration summary, resume/reset for abandoned shells); workspace
    bootstrap and `setup` re-login behind the same pane. Test: attach
    client tests for the shell route; wizard rendered against a stub
    shell.
12. **Review and land.** Diff tab: Pull branch, copyable review
    commands, close-as-merged/abandoned. Test: pull wired to a stub
    `/local/v1/pull`; close round-trips.
13. **Onboarding wizard.** Link, workspace init, repo picker + remote +
    push, fake-harness demo path. Test: wizard against stub local verbs.
14. **Sync overlay + daemon surfaces.** Run-detail toggle, settings
    card. Test: stub-backed state transitions, conflict surfaced from
    the `sync.conflict` event.

### Phase 3: the desktop shell

15. **Electron wrapper.** New `desktop/` directory: sidecar spawn and
    lifecycle (kill on quit, restart on crash), window loading the
    tokened URL, `contextIsolation`/`nodeIntegration` posture, external
    links to the OS browser.
16. **Notifications and badge.** Event-stream-driven native
    notifications for needs-attention transitions and new approvals;
    tray/dock badge from the attention count; clicking focuses the run.
17. **Deep links and updates.** `aether://` protocol registration;
    Electron auto-update for the shell; sidecar via `internal/selfupdate`.
18. **Packaging and release.** Signed macOS/Linux/Windows builds in the
    release workflow beside the existing binaries; install doc section.

### Phase 4: polish

19. **Multi-server profiles.** Config grows named links; the app gets a
    server switcher; CLI default-link behavior unchanged.
20. **Keyboard-first parity.** Palette verbs for every new surface;
    shortcut reference.
21. **Offline/reconnect UX.** Distinct SSH-down vs server-down states
    surfaced through the existing unreachable-panes pattern.

## Risks

- **The local gateway is a credential-adjacent process.** It holds an
  authenticated SSH connection. Loopback-only binding and the
  per-process token are non-negotiable from the first commit; refuse
  configuration that would widen either.
- **Two gateways, one SPA** stays cheap only if gating is data-driven
  (the capability descriptor). Any `isDesktop` conditional in a view is
  a review rejection.
- **Workspace-shell registration happens on clean exit.** The UI must
  make that path obvious and offer resume/reset; task 11 carries this
  explicitly.
- **Electron widens the supply-chain and update surface.** Scoped to the
  shell only; every privileged operation lives in the Go sidecar shipped
  through the existing release process.
- **Shared framing extraction (tasks 4-5) touches `internal/dashboard`.**
  Behavior must be provably unchanged: the existing gateway tests are the
  gate, not new ones.

## Verification gates

Per phase: `make fmt-check && make vet && make test && make public-audit`,
and from `web/`: `bun run typecheck && bun run test`.

- Phase 1 exit: E2E pass driving `aether gui` against a scratch server
  (dev binary, throwaway data dir, fake runtime) through link, launch,
  attach, event replay, and `session.new`; the browser path via
  `aether dash` re-verified unchanged.
- Phase 2 exit: the quickstart journey (link -> workspace -> agent add ->
  run -> review -> pull -> close) completed entirely inside the GUI
  against a real server with the fake harness; `make test-integration`
  green.
- Phase 3 exit: packaged app on each OS completes the same journey;
  notification fires on a run parking; update path exercised against a
  test release feed (`scripts/publish-release-test.sh`).
- Docs updated with the behavior they describe in the same phase:
  `docs/dashboard-api.md` (capabilities endpoint), a new
  `docs/local-gateway.md`, `docs/quickstart.md` (GUI path beside the CLI
  path), `docs/security.md` (local gateway stance), `docs/install.md`
  (desktop packages).
