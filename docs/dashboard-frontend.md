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
collapse state, `activeWorkspace`, grouping, dismissed update versions) are
persisted; server data is always re-fetched.

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
- **The header carries the way in.** New run opens the launch form (gated on
  `canLaunch`), the board button and All runs switch between the two
  whole-workspace views, and the group-by toggle sits beside them.
- **Below the runs, the nav links are capability-gated** on the method or local
  verb that powers each view, so a gateway that cannot serve a surface never
  shows the way in. All runs is the exception: it is a view of the runs the
  sidebar is already showing, so it needs no gate.

## Files view

`src/routes/files/` is the read-only repository browser. It groups each visible
workspace's base branch and live run checkouts in a lazy tree, caches each
directory request in `src/store/files.ts`, and reads file contents through
`files.read`. Run files can switch to a one-file `files.diff` patch; editing
stays in the existing local sync and the user's editor.

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
  the build banner (see the onboarding wizard section), and a `server.update`
  event lands in the `server` slice, which feeds the update prompts.

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
A workspace with nothing in it says so once rather than as three empty
columns: what a run is, and the New run button, because an empty board is the
first thing a new member sees.

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
`workspace.timeline` pause or resume arrives, and neither surface offers a
verb rather than offering the one the server would refuse.

## Commands: one list, two ways to reach it

`src/lib/commands.ts` holds every verb the dashboard can perform - the run
verbs (pause/resume, inject, close as merged or abandoned, kill,
protect/unprotect, relaunch, pull branch, hand off) and the board verbs
(open the board or the list, launch, launch from a template, mark all seen) -
as data: an id, a label, an icon, the capability gate, and the call itself.
`useCommandRunner()` performs one and reports the outcome the same way
everywhere: the gateway verbs toast their past-tense name or the server's
refusal verbatim, and nothing writes run state into the store, because the
event stream is what reports the result.

Two surfaces render that one list, so a label or a gate can never drift:

- **The command palette** (`src/components/palette/`) is the cmdk palette:
  `⌘K`/`Ctrl+K` anywhere, or the button it registers into the `statusbar`
  slot (it has no home of its own in the shell, and the dialog portals out of
  the status bar anyway). It jumps to runs and workspaces - opening a
  workspace also makes it the active scope, so the sidebar and the board
  follow - and steers **the run the center view is showing**, any run-detail
  tab, since it keys on `route.params.runId` rather than on a route name.
  From the board there is none, so reveal a run first.
- **Visible buttons**, so nothing important is reachable only by a shortcut:
  New run in the sidebar header, in the board header and in the notice an
  empty board carries above its columns; All runs in the sidebar nav; and the
  run action bar (`src/components/run-actions.tsx`) in the header of every
  run-detail tab, which is where the run verbs live for a member who has not
  learned `⌘K` yet.

Two things the buttons add. A `Command` carrying a `confirm` field - kill and
both closes - opens a dialog naming the run before it runs; the palette does
not ask, because a palette item is already several deliberate steps (open,
type, select) away from an accident, where a button is one click. And the bar
locks while a verb is in flight, showing a spinner on the one running: a pull
shells out to `git fetch` over SSH and takes seconds, and a second click would
race the first for the same ref. Buttons also take the command's `short` label
and keep the full sentence as their tooltip, because eight of them share one
`h-9` header row.

**Who may do what is asked twice.** `src/lib/permissions.ts` mirrors
`internal/permissions`: the role table, plus the two restrictions on top of it
(a protected run limits steer and kill to its owner and admins, and a
workspace with `steer_others=admins_only` does the same for others' runs).
The server is still the authority and checks every call again, but a button
one click from a denial is worse than no button, so the command list asks the
same questions first. A viewer sees nothing that mutates a run; hand off and
protect need the run's owner or an admin. Before hydration the caller's own
record has not arrived, and the mirror answers yes rather than making the
shell's buttons appear a beat late. Pull is the exception that is not a
question for this policy at all: it is the desktop gateway fetching into the
repository on this machine, so it answers to `hasLocal('pull')` alone.

### The forms

The three verbs that need prose open a dialog rather than calling straight
through: launch, inject, and launch from a template. The launch and inject
forms are a store dialog (`openPaletteDialog` on the `palette` slice) hosted
by `AppShell` through `components/palette/dialogs.tsx`, so a button on any
surface opens one by asking the store, with no dependence on the palette or
the status bar being on screen. The template form's open state lives with
`CommandPalette` in `index.tsx` instead, because the store's dialog union
knows only the other two. It lists the active
workspace's templates over `template.list` and starts the run with
`template.launch` (both on `lib/api.ts` like every other call), then reveals
it.

The launch form asks for a task, a harness and a mode. The task is optional in
interactive mode - a taskless launch drops the member into the agent's TUI
with no seeded prompt - and required in headless, which has no interactive
surface, so the form disables Launch and says why rather than sending a
request the gateway will refuse (`runLaunch` in `internal/sshd/handlers.go` is
the same rule). Only what was actually chosen goes on the wire: an empty task
and the default `tui` mode are the server's own defaults. The harness list
comes from `agent.list`, plus the always-offered `custom` escape hatch; a name
the server does not know is refused by the server, not by the form.

Neither launch form asks which workspace to launch into: both take
`activeWorkspace` and say where the run will land, naming the workspace and its
base branch. The switcher is the picker, so a second one inside the dialog
would be a place for the two to disagree.

Launching is gated on `run.launch` **and** on the launch permission
(`canLaunch`). The local gateway advertises every method regardless of who is
behind it, so capability alone would put the button in front of someone the
server would refuse.

## Terminal view

`src/routes/terminal/` is the run-detail Terminal tab: xterm.js over
`/ws/attach/<run>` (`docs/local-gateway.md`). The run-detail routes share one
tab strip (`tabs.tsx`), so Overview, Terminal, Diff and Events are registry
routes on the same `runId`.

The Terminal view is a vertical split. The agent terminal keeps the flexible
space above a `RunDock` below it. The dock has a persisted height
(`UiSlice.runDockHeight`, default 240px), a collapse toggle, and a resizer.
Its tab state and socket registry live in `src/store/terminal.ts`, so opening
Overview, Diff, or Events does not discard run-shell tabs or their attachments.
Only the selected shell tab mounts an xterm host; switching tabs remounts that
host and relies on transcript replay to restore its content.

- **The socket is `attach.ts`**, framework-free and the only part with logic
  worth testing. It reuses `backoff()` from `src/lib/stream.ts`, so the
  terminal and event stream reconnect on the same jittered schedule, and it
  splits large input (a paste) into several ordered frames under the gateway's
  64 KiB frame cap, never splitting a surrogate pair.
- **Mirror by default for the agent.** The agent header carries no `write` key
  unless the user asks to steer; the toggle reattaches rather than upgrading
  in place. Whether the member may steer is the server's answer, never the
  client's guess: a `-32001` refusal drops the request back to a mirror and
  disables the toggle. Every other refusal (unknown run, no live terminal)
  stops the reconnect loop and offers a retry.
- **Run-shell tabs always write.** The `+` control opens names `t1`, `t2`,
  `t3`, and `t4`; four is the per-run limit and the disabled control says
  `At most 4 tabs`. Each shell attach uses
  `/ws/attach/<run>?shell=<tab>`, requires write/steer permission, and closes
  its socket when the tab is closed. A `-32001` response does not reconnect;
  the dock replaces the terminal with the sentence **You can view this run
  but not open a shell in it**. A normal `1000` socket close removes the
  finished tab.
- **Every attach answers for itself.** The agent run slice is reset when the
  view mounts, and a successful attach clears the standing refusal. Otherwise
  a denial outlives the socket that produced it: leaving the tab and coming
  back would show a live terminal beside a stale error, with steering greyed
  out even after `run.handoff` granted it.
- **A 1008 close is read, not guessed at.** The server re-checks a live
  attach's authorization every few seconds, the gateway relays a loss as a
  1008 close, and the close reason names which gate fell: `steer permission
  withdrawn` just downgrades - the client reconnects immediately as a
  read-only mirror - while a dead token or `membership withdrawn` would refuse
  every reconnect, so those stop the loop and surface the reason. A refusal
  frame arrives with its own 1008 close, which is why the client reacts to the
  code only when no refusal preceded it.
- **Reconnect resumes with full recent history.** The gateway replays the
  recent transcript to every attach, and the client clears the buffer first,
  which keeps a reconnect from stacking a second copy of the scrollback under
  the first. The shared xterm host uses `scrollback: 50000`; the server replay
  ring is 1 MiB and is seeded from the cast tail when a session is restarted,
  so re-attach retains the full recent history rather than only 64 KiB.
- **DOM renderer, deliberately.** `@xterm/addon-webgl` 0.19.0 can reuse stale
  glyph-atlas positions under heavy glyph churn (xtermjs/xterm.js#6038), garbling
  scrolled rows until a forced refresh; the DOM renderer never desyncs. The
  terminals render in the shipped JetBrainsMono Nerd Font Mono
  (`src/lib/term-font.ts`, declared in `src/index.css`), so agent TUIs get
  their powerline and devicon glyphs at the same advance as text. The terminal
  opens only once regular and bold faces are loaded, because xterm caches glyph
  metrics synchronously at `open` and would otherwise bake fallback metrics in.
- **Injections need no client work.** The server writes the attributed
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
- **The verbs are not here, the answers are.** The tab keeps what is only
  about reading the diff - the refresh, the snapshot list, the two copyable
  `git` commands that review the run branch in the linked repository, and the
  output of the last pull (`review-commands.tsx`). Fetching the branch and
  closing the run are verbs, so they sit in the run action bar in the header
  with every other verb rather than a second time in the tab; the fetch output
  is an answer rather than a verb, so the pull records it on the `local` slice
  and the tab shows it where a member reviewing the branch will look.
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

`src/routes/onboarding/` is the guided first-run path, six steps: Link,
Workspace, Environment, Repository, Agents, First run. It renders only where
the gateway serves the client-machine verbs (the capability descriptor lists
`link.status`); a remote monitor gets an explanatory empty state instead of
a broken wizard. Link, Workspace, Repository and First run live in
`steps.tsx`; Environment is `environment-step.tsx`; Agents is
`agents-step.tsx` with its second half in `profile-import.tsx`.

The Repository step adds the `aether` remote (`link.repo`) and then seeds
the workspace: where the gateway serves `repo.push` it shows a **Push now**
button that runs the push in the clone. Success names the branch that
landed and keeps git's output in a "What git did" panel, open on arrival
because `Everything up-to-date` and `[new branch]` are both success and
mean different things; Continue then moves on. A refusal keeps the user on
the step with git's own output in a monospace block, both retry and the
copyable command still there. The branch is the workspace's base branch,
so a workspace created with `--base` seeds the branch its runs fork from.
An older gateway without the verb shows only the copyable command.

The UI slice persists the current step and selected workspace, and first open
routes an unboarded local gateway here. Completing the final step or
navigating elsewhere marks the UI onboarded and clears that wizard state.

The Link step distinguishes no configured server, a server with no repository,
and a fully linked server. It refreshes on Retry and when the window regains
focus, so a separate `aether link` command appears without restarting the GUI.
The first four steps' components live in `steps.tsx`; the Environment step is
`environment-step.tsx`.

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

The Agents step has two optional halves and never blocks: **Skip for now**
is reachable from every state, including an open setup shell and a failed
scan.

Part A lists the setup-capable harnesses from `env.harnesses` against
`agent.list`, saying for each whether it is installed on this machine and
whether the server lists that name. The copy states what those two signals
actually mean - every shipped harness is on `agent.list` whether or not this
member has logged one in, so the list is not a "set up" badge - and **Set
up** embeds the same `AgentWizard` the Agents page uses, driven with the
harness and workspace already known so it opens the `agent-setup` shell
without a form. A clean exit refetches `agent.list` and hands the harness to
the First run step, which preselects it.

Part B (`ProfileImport`) previews each harness configuration on this machine
with `profile.preview`, showing the category counts and, behind an expander,
every exclusion with its reason. Previews never run on mount: a profile root
can hold hundreds of megabytes, so the user presses **Look at what is here**,
the harnesses are walked one at a time with the current one named, and
**Stop** aborts the fetch - which cancels the request context and stops the
walk on the gateway, not just the waiting. A preview that fails shows its
error on that harness's row; only the `-32602` that means "this harness does
not sync a profile" is silent, and the "nothing to bring" line renders only
when every harness answered without one. Checkboxes start unchecked: approving calls
`profile.push` once per checked harness, one at a time, and a refusal lands
on its own row while the rest still run. A `blocked` preview gets no
checkbox at all - the row names the condition from `blocked_reason` and shows
the flagged file, and offers the `--allow-secret` command only for a scanner
finding, since a symlink escape has no override. That override is
deliberately not in the dashboard. Where a setup-capable harness is
installed locally, **Ask an agent** runs the `profile` scan over
`/ws/envscan`, streams the agent's output, and pre-checks what it
recommended with each one-sentence reason next to its row; the scan is a
proposal the user edits, and a failure leaves the manual path and both
buttons live.

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

## Update prompts

`src/components/update-banner.tsx` is where the dashboard says a binary is out
of date: it hosts the banners, with the CLI one in
`src/components/cli-update-banner.tsx` and the pieces they share in
`src/components/update-banner-shared.tsx`. It is mounted by `AppShell` above
everything else, because an out-of-date binary is about the whole app rather
than the view that happens to be open. The CLI and shell prompts need the
gateway to serve `update.check` - a remote monitor cannot update anything on
your machine - while the server prompt asks the server about itself and shows
wherever the member is an admin.

- **Two reads.** The host reads `update.check` once on mount
  (`docs/local-gateway.md`; the gateway caches the release lookup, so this
  costs no request to GitHub) and puts the answer on the `local` slice, which
  is also what the status bar reads. It reads `server.update_status` as well -
  any member may - and re-reads it on every reconnect and whenever
  `server.info` names a different version. The reconnect is the one that
  matters: a server that updates itself re-executes, so the socket drops and
  comes back, and that fresh status is what ends the banner and the notice.
  `connect()` re-hydrates on the same signal while an update is in flight,
  even with a cursor to replay from, because only a fresh `server.info` says
  the server came back on the new version. A read that fails is recorded, not
  swallowed: the banner then says it could not read the status, with a
  **Retry**, rather than claiming the server cannot update itself.
- **The CLI banner is for everyone.** It names the new version and the running
  one, says what updating costs - it replaces the `aether` binary on this
  machine and restarts the dashboard, taking attached terminals and any
  running sync session with it, while the runs keep going on the server - and
  offers **Update now**, the release notes, and a dismiss. What the button
  will do is decided before the click, from `update.check`'s `install_method`
  (`docs/local-gateway.md`): *direct* offers the button and nothing more;
  *admin-prompt* (macOS with a GUI session and a `cli_path` in a directory
  only root can write, such as `/usr/local/bin/aether`) offers the
  button and says, before the click: *macOS will ask for an administrator
  password: {cli_path} is in a directory this account cannot write to. The
  dialog is labelled osascript, the tool Aether asks through. Aether never
  sees your password.*; *manual* (Linux with a directory this account
  cannot write, Windows, or a macOS gateway the dialog cannot serve - the
  rule is in `docs/local-gateway.md`) offers no button and shows the
  command to run instead - `sudo aether update` with a copy button, or the
  release link where the platform has no self-update at all. Clicking Update calls
  `update.apply` and the banner goes to a restarting state; nothing else
  reconnects, because the existing `ConnectionError` page already owns a
  gateway that goes away. The done state names every binary the swap replaced
  and, on a single-box install where `aether-server` was one of them, the
  `restart_command` the gateway sends back: the server keeps running the old
  code until its unit restarts, and the CLI prints that same line. A `-32001`
  (denied) answer is the dialog cancelled or the password refused: the banner
  shows *Update cancelled, nothing was changed.* muted rather than as a
  failure, and the button comes back. Any other refusal is rendered verbatim -
  the gateway's own message, ending in the command to run where there is one -
  and the button becomes usable again.
- **The server banner is for admins, and it acts.** Capability is half the
  gate and the caller's role is the other half, the same rule the admin
  surfaces follow, so it needs `useIsAdmin()` as well. It shows the server
  version and the latest release side by side, and what it offers under that
  comes from `server.update_status`:
  - *capable*: **Update now** and **Update when idle**. Update now opens a
    confirm dialog that counts the runs active in this member's own run list -
    the server's definition of busy, so a paused run and one parked at
    needs-attention are not counted - and says what the restart costs: the
    runs keep going because the server reattaches to their containers, and
    attached terminals reconnect on their own. It then calls `server.update`
    with `when: "now"`. Update when idle sends `when: "idle"`, and the banner
    becomes *Update to vX scheduled by <member>, applies when no run is
    active* with a **Cancel** that sends `when: "cancel"`.
  - *not capable*: the documented unprivileged install. No buttons: the
    server's own reason, then the two commands to run on the server host with
    a copy button, as before. A gateway that does not carry the method keeps
    that banner from `update.check`'s `server_behind`, and says only what it
    knows - "The dashboard cannot update the server."
  - The scheduled state also names what the update is still waiting for
    (`status.waiting`), because a live terminal attach holds it back the
    same way a working run does.
- **The phases come off the feed.** `server.update` events land on the
  `server` slice through `applyEvent`, once per workspace and once more from
  the RPC result, so the slice keeps the furthest phase rather than the last
  one to arrive: *scheduled*, *applying*, *restarting* - the socket drops
  there, the reconnect re-hydrates and re-reads the status, and a status
  whose `server_version` is the version the phases were about clears the
  progress and ends the banner - or *failed*, which shows the server's error
  verbatim and falls back to the manual commands. Every phase is a row in
  the activity feed too, filterable as *Server updates*.
- **Everyone else gets one line.** A member who is not an admin sees
  *server update scheduled, terminals will reconnect briefly* (or *applying*)
  in the status bar while one is in flight, so a restart nobody explained
  does not read as an outage. A server that does not answer costs the CLI
  banner nothing: `update.check` still returns the CLI half with the failure
  in `server_error`, because the CLI is a binary on this machine and a dead
  SSH hop is no reason to hide that it is out of date.
- **Dismissal is per version and it persists.** `dismissedUpdates` on the `ui`
  slice records which version was dismissed for each banner and rides the same
  persisted preferences as the theme and the sidebar, so a dismissal survives a
  reload and the next release shows the banner again. It silences the offer,
  not an update already moving: a scheduled or applying server comes back
  regardless, because that banner is why the server is about to restart.
- **The status bar carries the badge.** The `aether {version}` label gets a dot
  when either update is available, and clicking it clears the dismissals so the
  banner comes back - the label is the only always-visible surface, so it is
  the way back to a banner someone dismissed by reflex.
- **The desktop shell has a banner of its own.** The SPA ships inside the CLI,
  but the Electron shell around it is whatever `aether gui build` last
  produced. `aether gui build` stamps the CLI version into the shell's
  `package.json`, `desktop/main.js` hands it to the renderer, and
  `desktop/preload.js` exposes it as `window.aetherDesktop.shellVersion`. When
  it differs from the `version` the capabilities descriptor carries, a third
  banner says the app is out of date and gives `aether gui build`. It is
  deliberately not nested in the CLI banner and not keyed on
  `update_available`: the way a shell goes stale is that the CLI *was* just
  updated, which is the moment no update is available any more, so gating it
  on one would hide it in the only flow it exists for. It renders on the shell
  stamp alone, so a browser tab never sees it.

## Styleguide

- **Tokens only.** See [styles.md](styles.md) for the landing palette in dark, neutral in light, and `--state-*` tokens; components use token classes and no hex literals.
  The exception is member attribution colour, which is data from the server and is
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
handoff targets, never a viewer. The buttons that render the same list are
covered where they live: the run action bar showing pause, resume or neither
as the pause state is known, asking before a kill and only then killing,
offering the hand-off targets who may own a run and no button at all when
there are none, and gating pull and relaunch; the sidebar offering New run to
a member who may start one and not to a viewer, and All runs opening the flat
list; the board header opening the launch form and an empty workspace saying
so once rather than three times; and the launch form refusing a headless run
with no task while sending nothing the server already defaults. The sidebar
also covers the switcher naming a sole workspace instead of offering a
picker, a switch rescoping the run list, and the attention badge counting.
The permission mirror is exercised through the bar rather than on its own: a
viewer is offered nothing that mutates a run, a collaborator may steer and
kill another member's run but not give it away or protect it, and a protected
run and an `admins_only` workspace both close steering to everyone but the
owner. Two more cover the shared runner: a refused kill toasts the server's
message verbatim, and a slow pull locks the whole bar, names the ref it
fetched and leaves its git output on the store for the diff tab. The shell
test clicks New run in the sidebar and finds the real form, which is what
proves the host is the shell's rather than the palette's. The team surfaces are driven through the same stub
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
