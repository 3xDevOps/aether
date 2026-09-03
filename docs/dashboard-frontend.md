# Dashboard SPA (`web/`)

The browser client the server embeds and serves. Bun installs packages and
runs the scripts, Vite bundles, React 19 + TypeScript render, Tailwind v4 and
shadcn/ui (new-york, neutral, CSS variables) style, Zustand holds the state.

The gateway it talks to is documented in `docs/local-gateway.md`; this guide
describes the dashboard's public route, store, and component structure.

## Commands

```sh
make dashboard   # bun install --frozen-lockfile && bun run build (from repo root)
make build       # the same, then the Go binaries
cd web && bun run dev        # dev server, proxying /api and /ws to a running server
cd web && bun run test       # vitest
cd web && bun run typecheck  # tsc --noEmit
```

`bun run dev` proxies to `http://127.0.0.1:8080` by default; point it
elsewhere with `AETHER_DASHBOARD=http://127.0.0.1:<port>` naming a running
`aether gui --port <port>`.

## Build pipeline and the embed

`web/embed.go` embeds `web/dist` with `//go:embed all:dist`, which fails to
compile against an empty directory. The build output is not committed, so the
invariant is kept by two placeholder files:

- `web/dist/.gitkeep` is committed - a clean checkout compiles the Go server
  before anyone has run the web build.
- `web/public/.gitkeep` is copied into `dist` by every Vite build, so emptying
  the output directory never breaks the next Go build.

`.gitignore` therefore ignores all of `web/dist` except `.gitkeep`. A binary
built without running the web build serves the gateway's "dashboard not built"
response rather than a blank page.

CI installs Bun with `oven-sh/setup-bun` (version pinned in `web/.bun-version`)
in every job that runs `make build` or `make release`, plus a `dashboard` job
that typechecks and tests the SPA on its own.

## The three extension seams

Later waves (run board, terminal, diff timeline, team surfaces) add views and
state without editing the shell.

**Route registry** (`src/routes/registry.ts`). A route file calls
`registerRoute('board', Board)` at module scope; `src/routes/index.ts` imports
it once for that side effect. The center view looks the current route up by
name and renders it with `route.params`. Navigation is a store action -
`navigate('run', { runId })` - not a URL router: the dashboard is a single
screen with a reveal path, and every surface routes through the same call.

**Store slices** (`src/store/`). One Zustand store composed of slice creators,
one file each (`server`, `workspaces`, `runs`, `members`, `terminal`, `board`,
`palette`, `approvals`, `presence`, `cost`, `timeline`, `diff`, `shell`,
`local`, `environment`, `ui`). A new feature adds a slice file and one spread in
`createRootStore`. Slices are typed against the whole root state, so a slice
may read another's data. Only view preferences (theme, sidebar width and
collapse state, `activeWorkspace`, grouping) are persisted; server data is
always re-fetched.

**`activeWorkspace` is the scope every surface reads.** It lives on the `ui`
slice and names the workspace the sidebar's run list, the board, launches,
templates, budget dialogs and the activity feed all act on. Empty means "all",
which is what the board falls back to before hydration has named one.
`setActiveWorkspace` carries an open `workspace` route along with it, so the
switcher can never say one workspace while the view beside it acts on another,
and `navigate('workspace', ...)` makes the workspace it opens the active scope
for the same reason.

Derived data (the sidebar's grouped run list, the attention-ordered run list)
lives in
`src/store/selectors.ts` as pure functions over a narrow input type, wrapped by
memoizing hooks in `src/store/hooks.ts`. Selectors that build new arrays must
not be passed to `useStore` directly. A view that owns its own derived shape
keeps it beside the view instead (`src/routes/board/selectors.ts`). The
sidebar and board inputs carry the pending-approval run set rather than the
raw inbox: `usePendingApprovalRuns` derives it once, subscribing on a stable
string key, so a run holding a pending request presents as needs-attention
everywhere the selectors are read - the sidebar, the palette, and the run
lists - while a byte-identical inbox refetch re-renders nothing.

**Slots** (`src/components/slots.tsx`). Where a route registry is too coarse -
something belongs *inside* a surface another ticket owns - the surface renders
`<Slot name="..." />` and contributors call
`registerSlot(name, id, Component)` at module scope. Registration order is
render order, `id` keys the render and makes a double registration an error.
The slots that exist:

| Slot | Props | Where it renders |
| --- | --- | --- |
| `card:badges` | `{ run }` | the run card's title row, after the paused and unseen markers |
| `card:chips` | `{ run }` | the wrapping row under the task, beside harness and branch |
| `card:footer` | `{ run }` | the card's bottom row, right of the owner and timestamp |
| `statusbar` | none | the status bar, left of the theme toggle |

Card slot content sits above the card's click overlay, so a contributor may
render its own links and buttons; everything else on the card is one target
that reveals the run. Conflict chips () and watcher avatars and approval
badges () belong in these slots, not in `run-card.tsx`.

## Sidebar

`src/components/shell/sidebar.tsx` is a workspace switcher over a flat list of
that workspace's runs. There is no run tree: one workspace is in view at a
time, so the outer level had nothing left to hold, and the runs group instead
by state or by owning member (the `groupBy` preference, toggled in the header
and persisted). The only collapse state left is the whole sidebar's.

- **The switcher sits above everything it scopes**, and appears only when there
  is a choice: a single workspace renders as a plain label with its base branch
  under it, because a picker with one option is a control that cannot be used.
- **The runs come from `sidebarRuns`/`sidebarGroups`** in
  `src/store/selectors.ts`, filtered to `activeWorkspace` and sorted
  worst-state-first, then most-recently-changed-first, so what needs a human is
  at the top of whichever group it is in. An empty scope shows every run, which
  is what the list falls back to before hydration has named a workspace.
- **The attention badge counts, it does not navigate.** The runs below are
  already sorted worst-first, so the number is for a scrolled sidebar or a
  stall that landed while the member was elsewhere in the app.
- **Below the runs, the nav links are capability-gated** on the method or local
  verb that powers each view, so a gateway that cannot serve a surface never
  shows the way in.

## Title bar

`src/components/shell/title-bar.tsx` is the desktop shell's window chrome.
The Electron window is frameless, so the SPA draws the bar itself: 36px tall,
the Aether mark and the `aether` wordmark in VT323 (the landing page's display
face, loaded in `index.css` as `--font-pixel` and used nowhere else), and -
on Windows and Linux, where the shell draws no native buttons - minimize,
maximize/restore and close wired to `window.aetherDesktop.controls`. The bar
is `-webkit-app-region: drag` and every button `no-drag`; on macOS the native
traffic lights are kept and the bar reserves 78px for them instead of drawing
buttons.

`App.tsx` mounts it above the whole app, the `ConnectionError` page included:
that page replaces the shell, and a frameless window without a title bar would
leave an offline user unable to move or close the app. In a browser
`window.aetherDesktop` is absent, the component renders nothing, and the tab
keeps the browser's own chrome.

## Data flow

`connect()` in `src/store/sync.ts` owns the whole lifecycle. One round of HTTP
fetches hydrates the store (`server.info`, `workspace.list`, `member.list`,
`run.list`, `run.overlaps`, and `GET /api/v1/capabilities`), then `/ws/events`
is the only thing that changes it. Hydration also repairs the scope: an unset
`activeWorkspace`, or one naming a workspace that is gone, falls back to the
first by ID rather than leaving every scoped surface pointed at nothing. The capabilities fetch may fail without
failing hydration - a legacy gateway has no such endpoint - and the store
then holds `null`. The snapshot also seeds the board's paused map from each
run's wire `paused` field, skipping runs that do not carry it.

- **The subscription is established first.** Hydration starts only once the
  server acknowledges it (`{"ok":true}`), which is also when the client calls
  itself live. Otherwise a change between the snapshot and the subscription
  would fall in the gap and never be delivered or replayed.
- **Events arriving during a fetch wait in the queue**, and are applied once
  the snapshot lands, so an older snapshot never overwrites a newer event.
- **Events are applied one at a time, in sequence order**, each fully resolved
  before the next begins. The cursor is a single number, so it must never move
  past an event still waiting on a fetch.
- **The stream subscribes live on the first connect** (the fetch behind it
  provides the current state) and **replays from the highest applied `seq` on
  reconnect**, with jittered backoff. An event at the cursor is ignored, so a
  replay is idempotent; one strictly *below* it means the server's event log
  restarted (a recreated or restored data dir), and the client zeroes its
  cursor and takes a fresh snapshot rather than silently dropping everything
  the new log sends.
- **A reconnect with no cursor cannot replay** - on a quiet server nothing has
  advanced `seq` - so the client re-fetches the snapshot instead of
  subscribing live and silently missing the outage.
- **A failed hydration retries** on the same backoff, and the affected panes
  say the server is unreachable rather than animating skeletons forever. That
  generic copy never overwrites a more precise error already recorded.
- **A total failure replaces the shell with one error page.** When nothing has
  hydrated and an error is recorded, `ConnectionError` takes the window
  instead of an empty sidebar and an empty board behind a toast. Which hop
  failed picks the copy, because each one has a different fix: `network` says
  this computer is offline (reconnect wifi or the VPN), `server` says the
  server did not answer over SSH, `gateway` says the local `aether gui`
  process stopped answering, and a dead token says to mint a new link. The
  gateway's own message is kept behind a collapsed "Technical details", and
  the page suppresses the toast that would otherwise repeat it. Its Retry
  button clears the connection state and remounts the subscribe-and-hydrate
  cycle, rather than reloading the page and dropping the in-memory token.
- **A `1008` close naming a dead token stops the stream for good.** The
  gateway closes `1008` for a refused subscribe or a transient membership
  check too, which the next reconnect can outlive and so are retried; only
  `dashboard token revoked or expired` is terminal, because reconnecting
  would carry the same dead token. The panes then say to open a fresh link
  with `aether gui`, which is the whole fix: the token is minted per
  process, so a page that outlived its `aether gui` needs a new one.
- A `run.status` event for a run the client has never seen fetches that run
  before the event is applied, which is what keeps two quick transitions of a
  brand new run in order. If the fetch fails the event is unresolved: the
  cursor stays put and a fresh snapshot is taken to repair the store. An event
  naming an unknown workspace re-fetches `workspace.list`, because workspaces
  arrive only by fetch and a run under an unknown one would render nowhere. An
  event whose actor is not in the members map re-fetches `member.list` for the
  same reason: no `member.*` event exists, so a teammate who joined after
  hydration would otherwise render as a raw ID forever. A
  `workspace.timeline` entry of kind `handoff` re-reads its run the same way,
  because a handoff publishes no `run.status` event to carry the new owner.
  An `environment.build` event lands in the `environment` slice, which feeds
  the build banner (see the onboarding wizard section).

**The capabilities descriptor is the transport seam.** The store holds the
`GET /api/v1/capabilities` answer (`gateway`, `methods`, `ws`, `local`), and
`useCapability()` in `src/store/hooks.ts` wraps it as three predicates -
`hasMethod`, `hasLocal`, `hasWS` - with `methods: ["*"]` meaning everything.
When the descriptor is `null` (a gateway that predates the endpoint), the
fallback is the read-and-steer method set every gateway has always served,
`events` and `attach` sockets, and no local verbs, so an unknown gateway
degrades to monitoring rather than to "everything". Views gate on these
predicates rather than sniffing the URL, which is what lets the same SPA
render against a gateway with or without the local surfaces
(`docs/local-gateway.md`).

**Capability is half the gate; the caller's role is the other half.**
Transport capability answers what the gateway can carry, not what this member
may do, and the local gateway advertises `methods: ["*"]` - so gating on
capability alone put Invite, Approve and Remove in front of a collaborator who
then learned the truth from a `403`. `useSelfRole()` and `useIsAdmin()` in the
same hooks file read the role off `server.info`'s member record, and every
admin affordance now needs both predicates: the gateway can carry the method
*and* the caller holds the admin role. Reads are gated on capability only, so
the roster itself is reachable on the remote dashboard - `member.list` is
allowlisted, `member.approve` is not, and both the sidebar link and the
palette's Go-to entry gate on `member.list`. A non-admin gets the roster
read-only, with the header saying so rather than leaving them to infer it from
absent buttons.

Every request goes through `src/lib/api.ts` - the only module that knows route
shapes, the bearer token, and error decoding. It carries exactly the methods
the views call; the team-feature methods arrive with the tickets that use them.
Every call is a `POST /api/v1/<method>` bar three `GET`s - the diff tab's
patch text, the status bar's disk number, and the capabilities probe -
because those read a working tree, a filesystem, and the gateway descriptor
rather than RPC methods.
The token arrives as `?token=` in the URL `aether gui` opens (or prints with
`--url`), moves into session storage, and is sent as
`Authorization: Bearer` on HTTP and as `?token=` on WebSockets. It is minted
per `aether gui` process: no TTL, nothing to revoke, and it stops working
the moment that process exits.

The status bar's disk gauge renders when `server.info` carries a `disk`
object (`used_bytes`, `total_bytes`). That field does not arrive with
`server.info`: `protocol.ServerInfoResult` is shared with the CLI and frozen,
so the gateway serves the number on `GET /api/v1/disk` and the team refresh
writes it onto the stored info, which is the gauge's only reader. The field
stays optional and the gauge stays hidden if the read fails. What `statfs`
answers is the whole filesystem holding the data directory, not the directory
itself, and the gauge is labelled as that: it is the number that says whether
the box is running out of room, and claiming it as Aether's own usage would
be an invention.

## Run board

`src/routes/board/` is the default center view: the active workspace's run
cards in the three buckets the GUI spec copies from Orca. `needs-attention` is
Needs You (both stalls and clean exits waiting on `run.close` - the card's
reason line tells them apart), `queued`/`provisioning`/`running` is Working,
the terminal statuses are Done.
An active run whose approval request is still pending also presents as
needs-attention on the board and in the sidebar - the pause is invisible in
the domain status, so `runState` takes a pending flag fed from the approval
inbox. Cards sort by last state change, newest first; the columns are
untinted and the card carries the state colour.

Three buckets, and no Idle column: a card is a run, and no run status maps
to idle, so an idle column could only ever hold something that is not a card.
A workspace with nothing running is an empty board under a switcher that names
it, which says the same thing.

Two things the buckets do not come from the run status alone:

- **Paused** is a badge, not a bucket. A paused run still reads `running` in
  the domain enum, so the wire `Run` carries a `paused` field the gateway
  decorates from the scheduler on `run.get` and `run.list`, never derived
  from the stored run. The hydration snapshot seeds the board's map from it
  (`seedPaused`, skipping runs without the field - a legacy gateway), and
  live `pause`/`resume` entries on the `workspace.timeline` event stream keep
  it current (`pausedFromTimeline` in `src/store/board.ts`).
- **Unseen** marks a run whose state changed since someone acknowledged it.
  An ack records the status *and* the change time, because `stateChangedAt` is
  recomputed from the run's timestamps on every fetch and can move backwards -
  a needs-attention run has no `finished_at`, so a re-hydration falls back to
  `started_at`, and a time-only comparison would mute exactly the card that
  needed the human. Acks live in the board slice and are app-wide: `navigate()`
  acknowledges whenever the route it is given carries a `runId`, so every
  surface that reveals a run mutes its sidebar row and its board card together,
  and the board header marks everything at once. They last only as long as the
  tab - nothing is acknowledged when the page loads, so a fresh tab shows what
  is waiting rather than remembering that yesterday's you looked at it.

### Reason and paused on the wire

**The Needs You reason survives a fetch.** `protocol.Run` carries `reason` -
the last `run.status` reason, persisted with the run and sanitized
server-side - so a run that was already in needs-attention when the tab
loaded shows its reason line, and the card tells a stall from a clean exit
waiting on `run.close`. `toRecord` in `src/store/runs.ts` prefers the wire
reason and falls back to the previously stored one only when the fetch
omits it and the status has not changed (a legacy gateway); a live
`run.status` event still overwrites it with the event payload's reason. An
approval pause keeps its fallback: a card with an empty reason uses its
oldest pending request's action as the summary.

**The paused badge hydrates from the same snapshot.** With `paused` on the
wire (above), a reload shows the badge for a run paused earlier, and the
palette offers the right one of pause/resume. Against a legacy gateway
whose runs carry no `paused` field the state stays unknown until a live
`workspace.timeline` pause or resume arrives, and the palette offers
neither verb rather than the one the server would refuse.

## Command palette

`src/components/palette/` is the cmdk palette: `⌘K`/`Ctrl+K` anywhere, or the
button it registers into the `statusbar` slot (it has no home of its own in the
shell, and the dialog portals out of the status bar anyway). It jumps to runs
and workspaces - opening a workspace also makes it the active scope, so the
sidebar and the board follow - carries the board's own commands, and steers
**the run the center view is showing** - any run-detail tab, since it keys on `route.params.runId`
rather than on a route name. From the board there is none, so reveal a run
first. The
two verbs that need prose, launch and inject, open a dialog; the rest call the
gateway directly and let the event stream report the result, so no palette
action writes run state into the store. A third dialog launches from a
template: it lists the active workspace's templates over `template.list` and
starts the run with `template.launch` (both on `lib/api.ts` like every other
call), then reveals it. Its open state lives with the dialog host in
`index.tsx`, not in the store's dialog union.

The launch form lists the harness names from `internal/harness` because there
is no `harness.list` control-channel method. A name the server does not know
is refused by the server, not by the form.

Neither launch form asks which workspace to launch into: both take
`activeWorkspace` and say where the run will land, naming the workspace and its
base branch. The switcher is the picker, so a second one inside the dialog
would be a place for the two to disagree.

## Terminal view

`src/routes/terminal/` is the run-detail Terminal tab: xterm.js over
`/ws/attach/<run>` (`docs/local-gateway.md`). The run-detail routes share one
tab strip (`tabs.tsx`), so Overview, Terminal, Diff and Events are registry
routes on the same `runId`.

- **The socket is `attach.ts`**, framework-free and the only part with logic
  worth testing. It reuses `backoff()` from `src/lib/stream.ts`, so the
  terminal and the event stream reconnect on the same jittered schedule, and
  it splits large input (a paste) into several ordered frames under the
  gateway's 64 KiB frame cap, never splitting a surrogate pair.
- **Mirror by default.** The header frame carries no `write` key unless the
  user asks to steer; the toggle reattaches rather than upgrading in place.
  Whether the member may steer is the server's answer, never the client's
  guess: a `-32001` refusal drops the request back to a mirror and disables the
  toggle. Every other refusal (unknown run, no live terminal) stops the
  reconnect loop and offers a retry.
- **Every attach answers for itself.** The run's slice entry is reset when the
  view mounts, and a successful attach clears the standing refusal. Otherwise a
  denial outlives the socket that produced it: leaving the tab and coming back
  would show a live terminal beside a stale error, with steering greyed out
  even after `run.handoff` granted it.
- **A 1008 close is read, not guessed at.** The server re-checks a live
  attach's authorization every few seconds, the gateway relays a loss as a
  1008 close, and the close reason names which gate fell: `steer permission withdrawn` just downgrades - the client
  reconnects immediately as a read-only mirror - while a dead token or
  `membership withdrawn` would refuse every reconnect, so those stop the loop
  and surface the reason. A refusal frame arrives with its own 1008 close,
  which is why the client reacts to the code only when no refusal preceded it -
  and a refusal frame that itself names the dead token stops the loop like the
  close reason would, rather than being read as a steer denial.
- **Reconnect resumes.** The gateway replays the recent transcript to every
  attach, so a reconnected pane is never blank; the client clears the buffer
  first, which is what keeps a reconnect from stacking a second copy of the
  scrollback under the first.
- **DOM renderer, deliberately.** `@xterm/addon-webgl` 0.19.0 can reuse stale
  glyph-atlas positions under heavy glyph churn (xtermjs/xterm.js#6038),
  garbling scrolled rows until a forced refresh; the DOM renderer never
  desyncs. The terminals render in the shipped JetBrainsMono Nerd Font Mono
  (`src/lib/term-font.ts`, declared in `src/index.css`), so agent TUIs get
  their powerline and devicon glyphs at the same advance as text - a
  symbols-only overlay font renders them full-em wide and xterm's per-cell
  letter-spacing slices the overhang off. The terminal opens only once
  regular and bold faces are loaded, because xterm caches glyph metrics
  synchronously at `open` and would otherwise bake fallback metrics in.
  The workspace-shell pane (`routes/shell/pane.tsx`) uses the identical
  renderer and font setup.
- **Injections need no client work.**  writes the attributed
  member-coloured banner into the PTY stream itself, so it arrives as ANSI and
  xterm renders it like any other output.
- Board cards get no live terminal previews in v1 (spec cut-line).

The terminal's colours are the one place the tokens cannot be used directly:
xterm needs resolved theme values rather than the CSS variables, so the view
reads the computed background and foreground off its own host element and
re-reads them when the dark class on `<html>` changes.

## Run events tab

`src/routes/terminal/events.tsx` is the run-detail Events tab: the workspace
activity feed pinned to the run in view. It drives the same feed slice and
paging readers the team activity view uses (`openFeed`, `drain`,
`olderFeed`), and both views render rows through the one shared component
(`src/components/feed-entry.tsx`), whose describe covers every feed payload -
`run.agent` and `run.diff` included - so the two feeds cannot drift apart.
Because the slice is shared, the pin is borrowed: the tab captures the
filters on mount and restores them on unmount, so the team Activity view
opens with whatever it had chosen.

## Diff timeline and conflict chips

`src/routes/diff/` is the run-detail Diff tab, and `src/store/diff.ts` holds
both what it renders and the overlap set the conflict chips read.

- **The patch is fetched, the events only say when.** `run.diff` carries
  per-file stats and no text, so a snapshot bumps the run's `revision` and the
  tab re-fetches `GET /api/v1/run/<id>/patch` (`docs/local-gateway.md`)
  whenever that has moved past the `fetched` revision the stored patch answers
  for. Counters rather than a stale flag, because a snapshot landing *during*
  a request would write true over true and then be cleared by the response:
  the answer records the revision it was issued at, and anything newer asks
  again instead of showing a diff that is behind while calling itself fresh.
  A failure records the revision too, so it cannot spin; the next snapshot or
  the Refresh button asks again.
- **The snapshots say when files changed, not what each interval changed.**
  The chronological list is the `run.diff` events themselves - time, file
  count, totals - and the patch beside it is always the run's current diff
  against the fork point. Selecting a snapshot narrows that patch to the files
  the snapshot touched; the selection keys on the snapshot's timestamp, so a
  new snapshot prepending to the list never retargets it. The list is capped
  at 40 per run and starts empty on
  every page load, because there is no history to replay. See the gap below.
- **Colour is the whole of the highlighting.** `parse.ts` splits the unified
  diff into files, hunks and line kinds; `patch-view.tsx` paints those kinds.
  The dashboard never edits code, so there is no editor and no language
  grammar - the core spec's cut-line, and why neither Monaco nor CodeMirror is
  a dependency. A truncated patch parses to a last file with fewer lines, and
  the view says the diff was cut short rather than failing.
- **Conflict chips are advisory.** `conflict-chips.tsx` registers into
  `card:chips` and the Diff tab renders the same component in its header. It
  reads the overlap set the conflict radar reports (`run.overlaps` at
  hydration, then `run.overlap` events), names the file and the other member,
  and navigates to their run. The event payload names peer runs but not their
  owners, so attribution comes from the runs the store already holds; an
  empty peer list means the overlap cleared and the chip goes.

### One gap, waiting on the wire

**There is no per-interval diff, so the tab does not claim one.** The GUI spec
describes this surface as a chronological list of unified diffs - "what
changed in the last five minutes". What can be built today is one patch, the
whole diff against the fork point, filtered by a snapshot's file list: a file
edited at snapshot 1 and again at snapshot 3 shows its state now under either,
and a file that was reverted drops out of the current diff altogether. The
delta per interval is unobtainable from what the server records - `run.diff`
carries numstat stats and nothing else, and no tree is kept per snapshot - so
closing it means the diff snapshot engine writing a tree (or a patch) per
snapshot and an endpoint to read one back. Filed as the related issue. Until then the
header reads "Current diff against `<base>`" and the list is headed "When
files changed", because a label that promised the interval would be the actual
defect.

## Team surfaces

`src/routes/team/` is presence, the shared approval inbox, the workspace
activity feed and budgets - the four readouts of the team features
(`internal/approvals`, `internal/timeline`, `internal/cost`). None of them owns
a view of its own in the shell: they reach the run card and the status bar
through the slots those surfaces expose, and the two full views are registry
routes (`approvals`, `timeline`).

- **They refresh from the event cursor, not a timer.** Every event the store
  applies advances `lastSeq`, and that is the only signal available that a
  teammate may have changed one of these reads - the gateway has no push
  channel for a roster or a queue. `useTeamRefresh` in `src/routes/team/sync.ts`
  re-reads them when the cursor moves, with a floor between refreshes so a
  chatty run does not become a request per event. It is mounted from the
  status-bar contribution, the one surface that is always on screen, which is
  also where the presence heartbeat lives.
- **One refresh covers every workspace, and there is only the one.** These
  reads are per workspace on the wire, and a workspace is a repo plus its
  environment plan: a deployment has a handful of them and they outlive every
  run in them. So `refreshTeam` reads all of them each time rather than
  splitting into a bounded recurring pass and a wide occasional one. Both
  readouts it feeds ask a whole-deployment question anyway - the status bar
  claims the worst budget state anywhere, and the badge claims the size of the
  whole queue - and a workspace does not stop being over its cap or holding an
  undecided request when its last run finishes, so no subset could answer
  either one. Failures leave the last good data in place.
- **The heartbeat is narrower.** It claims only the workspace in view -
  `focusedWorkspace` prefers the route's `workspaceId`, then the workspace of
  the run in view, then `activeWorkspace` - because presence is keyed on
  (member, workspace) and beating them all would report you online to
  teammates in workspaces you have never opened.
- **The queue is every workspace's, in one list.** The inbox view reads them
  all again when it opens, because it is the surface that shows the requests
  themselves rather than a count.
  Each row names the workspace the request belongs to, since the list crosses
  them.
  Decisions go through `approval.decide` with the run the request belongs to,
  so the server attributes them and applies the steer check: a refusal is
  rendered as the server's answer, never predicted by the form. A request the
  user has just decided stays on screen reporting its outcome, laid over the
  fetched queue, because the next fetch no longer returns it.
- **The feed opens at the end of the log.** `workspace.timeline` pages forward
  from a cursor only, so the view first asks for a page past the end - that
  answer carries the log head - and opens a window back from it; the live tail
  is the same paging call from the cursor the window reached. "Load older"
  reads the new stretch only, up to where the previous window began, keeping
  what is already loaded: re-reading the whole widened window would spend the
  page budget on history the feed already has and lose the newest end of it.
  Pages merge into the feed in sequence order however they arrive, so the
  window stays oldest-first.
  When that budget does run out the view says so rather than stopping
  quietly. Every open stamps the read, so pages still arriving under the
  filters the user just left write nothing. Actor dots are the member's own
  colour from the member payload. This is the one scoped surface that keeps a
  workspace picker of its own, because comparing what happened in one workspace
  against another is the question the view exists to answer; it opens on the
  active workspace and switching it clears the run filter, since a run belongs
  to exactly one workspace.
- **A budget warns, it never stops anything.** The status bar shows the spend
  and the worst state any workspace is in (`ok`, `warn`, `exceeded`) - every
  workspace, ones with nothing running included, which is what the wide read
  above is for - and says so in those words, naming each workspace and its cap
  in the tooltip. A spend that includes unmetered runs renders as a floor
  (`$1.20+`), because a harness with no adapter reports nothing.
- **The two admin dialogs live on the workspace view, not the status bar.**
  `routes/workspace.tsx` is one workspace: its name, its base branch, its runs,
  and buttons for the budget (`budget.set`) and the steering policy
  (`workspace.settings`), each gated on `hasMethod` for the method behind it. A
  spend ceiling and a steering policy belong beside the thing they govern
  rather than in a header two views away. The settings dialog shows the base
  branch without offering to change it: runs have already forked from it, so
  editing it there would only make the displayed branch disagree with the
  branches on disk.
- **The Environment panel sits above the run list.**
  `routes/workspaces/environment.tsx` renders in the workspace view's
  scrolling body wherever the gateway serves `env.status`: the active
  version's manifest as a plain list (name, version, reason), one sentence
  saying which path made it and with which agent, a compact version history
  with the failure detail on failed rows, and rollback behind a confirm that
  names the version it returns to - the newest good version below the active
  one, the same pick the server makes. A workspace with no definitions gets
  one sentence: it uses the image it was created with. The panel re-reads
  `env.status` whenever this session's build state moves, so an approved
  build's outcome lands in the history without a reload.
- Watcher avatars come from the roster's `watching` set, which the gateway
  fills from live PTY attaches - the browser's attaches included.
- The same refresh reads `GET /api/v1/disk` and writes it onto the stored
  `server.info`, which is what fills the status bar's disk gauge.

## Onboarding wizard

`src/routes/onboarding/` is the guided first-run path, five steps: Link,
Workspace, Environment, Repository, First run. It renders only where the
gateway serves the client-machine verbs (the capability descriptor lists
`link.status`); a remote monitor gets an explanatory empty state instead of
a broken wizard. The first four steps' components live in `steps.tsx`; the
Environment step is `environment-step.tsx`.

The Repository step adds the `aether` remote (`link.repo`) and then seeds
the workspace: where the gateway serves `repo.push` it shows a **Push now**
button that runs the push in the clone. Success names the branch that
landed and keeps git's output behind a "What git did" expander, because
`Everything up-to-date` and `[new branch]` are both success and mean
different things; Continue then moves on. A refusal keeps the user on the
step with git's own output in a monospace block, both retry and the
copyable command still there. The branch is the workspace's base branch,
so a workspace created with `--base` seeds the branch its runs fork from.
An older gateway without the verb shows only the copyable command.

The Environment step offers two cards: mirror this machine (recommended,
preselected) and keep the standard environment the workspace was created
with. Mirror lists the setup-capable harnesses found on this machine
(`env.harnesses`) by friendly name, runs the chosen one headless over
`/ws/envscan` behind a one-line status with a collapsed "View process"
expander streaming the raw agent output, and hands the validated Dockerfile
and manifest pair to the review gate; when no supported CLI is installed the
card says so and names the four. Cancel and every scan failure land on "try
again" or "keep the standard environment", so the wizard never dead-ends.
Non-admin members see only the keep path, because saving an environment is
an administrator method.

The review gate (`EnvironmentReview`, same file) renders the manifest as a
readable list - name, version, reason - with a per-item remove toggle
backed by `removeManifestItem` (dropping an item drops its Dockerfile lines
and shifts later spans; the last remaining item cannot be removed). A
free-text change request reopens the scan in refine mode with the current
pair and the note; approve calls `env.save` (source `mirror`, the chosen
harness) then `env.build` and advances the wizard immediately - the build
runs in the background.

Build state lives in the `environment` slice: approve primes it before the
build call so no event frame can beat it, and `environment.build` events
applied by `sync.ts` drive it from there, ignoring frames about older
versions. The slice keeps the approved pair because `env.status` never
returns the Dockerfile: a verification failure can seed its repair scan
only from what this session holds. `EnvironmentBanner` (same file, rendered
by the First-run step and the run Overview view) reads the slice: while the
latest build for the workspace is pending it says the environment is still
building on the starter image; on `active` it clears; on `failed` it shows
the detail and offers "ask the agent to fix it" - a refine scan seeded with
the failure detail, feeding the same review gate - plus "keep the standard
environment", which just forgets the build, since the workspace image
already is the fallback. Nothing in the slice persists: after a reload the
banner is simply gone, and `aether env show` is where the build's outcome
can still be read.

## Styleguide

- **Tokens only.** Colours live in `src/index.css`: the shadcn neutral base
  plus `--state-*` tokens for the presentation states. Components use token
  classes (`bg-state-working`, `text-muted-foreground`); no hex literals. The
  exception is member attribution colour, which is data from the server and is
  applied inline.
- **Dark, light, system.** The preference is stored, `system` follows
  `prefers-color-scheme` live.
- **Two glyphs, two layers.** The harness glyph says who is running, the state
  dot says what state - never merged into one mark. Presentation states
  (`working`, `waiting`, `needs-attention`, `failed`, `done`, `idle`) are
  derived in `src/lib/status.ts`; the domain status enum is untouched. A group
  header shows the worst state of the runs under it.
- **Member colour attributes, it does not fill.** The avatar rings itself in
  the member's colour and keeps its initials in the foreground token, because
  the colour is arbitrary server data with no contrast guarantee in either
  theme.
- **Loading feedback matches duration.** Skeletons appear only after ~200ms
  (`useDelayed`), so a fast response never flashes.

## Tests

`vitest` with jsdom and testing-library. The store slices and selectors are
tested directly; the sidebar and the whole shell are rendered against a
hydrated store with a stub API (`src/test/fixtures.ts`) to prove they follow
live data. The stream and the hydrate/stream lifecycle are driven through a
stub WebSocket (`src/test/stub-socket.ts`): subscribe frames, replay after a
dropped socket, the 4000 close, hydration waiting on the subscription
acknowledgement, the mid-hydration event, the cursor held behind an unresolved
fetch, the cursorless reconnect, and the hydration retry. The terminal is driven through the same
stub: the attach client's own tests cover the header, the reconnect and the
refusals, and the view is rendered with a real xterm instance to prove
the toggle and the steer refusal reach the UI. The board and the palette are
rendered against a seeded store: bucket membership and ordering, the board
showing only the active workspace and following a switch, the board falling
back to every run before hydration has named one, the ack muting a card and a
later state change bringing the emphasis back, the paused badge going on and
off through a real `workspace.timeline` pause and resume run through
`applyEvent`, a slot contributor reaching the card, and the palette jumping,
switching the active workspace and opening it, steering from a run-detail tab,
withholding both pause and resume while the paused state is unknown, launching
into the active workspace, and offering only members who can own a run as
handoff targets, never a viewer. The sidebar covers the switcher naming a
sole workspace instead of offering a picker, a switch rescoping the run list,
and the attention badge counting. The team surfaces are driven through the same stub
API: the status bar reading roster, queue and budget and rendering all three,
the approval badge and watcher avatars reaching a real run card, a decision
going out as `approval.decide` and coming back attributed, a steer refusal
surfacing instead of being guessed at, the refresh covering every workspace
rather than only the ones with live runs, the heartbeat claiming only the
workspace in view, an over-cap workspace staying in the readout after its last
run finishes, and the feed opening its window at the log head, walking it back
without re-reading, narrowing on a filter, and abandoning a page that belongs
to filters the user has left. The Members roster is rendered both ways: an
admin approving, inviting and changing another member's role with the roster
refetching after, the server's refusal rendered verbatim when a role change is
denied, the confirmation an admin must clear before giving up their own admin
role, and a non-admin getting the same roster as read-only text with no admin
verbs - which the sidebar and the palette match by keeping Members reachable
behind the narrow remote allowlist while every other admin entry stays
hidden. The onboarding wizard walks all
five steps against the stub API, and the environment step is exercised on
both the walk and its scan flow - mirror preselected, the no-harness
fallback, streamed output behind the expander, cancel, failure landing on
the fallback offers, the scan result reaching the review boundary, and the
non-admin keep-only path - through a stubbed scan session. The review gate
and the build banner ride the same stubs: a removal shrinking the approved
payload, approve saving then building and priming the slice, a change
request reopening the scan in refine mode, the banner appearing on building
and clearing on active in both the First-run step and the run view, and a
verification failure offering the repair scan and the dismissal. The diff tab covers the parser on the
shapes that would break it - a deletion, a new file, a removed line that reads
exactly like a file marker - then the fetch, the truncation notice, a snapshot
refetching and narrowing the patch, and a conflict chip naming its member and
opening their run. Full end-to-end coverage of the dashboard belongs to the
E2E harness driving the real gateway.
