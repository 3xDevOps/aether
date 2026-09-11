# Dashboard SPA (`web/`)

The browser client is one static bundle served through either dashboard
gateway: `aether gui` on the user's machine, or
`aether-server --web-port` over HTTPS on the server's tailnet addresses.
Next.js 16.3.4 produces the static export, while React 19 + TypeScript render
the client runtime, Tailwind v4 and shadcn/ui primitives with CSS variables
provide the base style, selected HeroUI v3 wrappers provide Chip and Tooltip,
and Zustand holds the state. Both gateways use the shared `internal/webgate`
API and WebSocket surfaces; the server gateway authenticates each request with
Tailscale WhoIs and no browser token.

The visual contract is a VS Code-inspired developer workbench, not an
official reusable VS Code component package. It follows VS Code Dark Modern
and Light Modern semantics, dense flat panes and compact controls while
preserving Aether's routes, capabilities, run states and startup behavior.

The gateways and their transport boundaries are documented in
`docs/local-gateway.md`. The same bundle serves both, gating machine-local
surfaces on the capabilities descriptor while keeping shared Files,
configuration, runs and terminal surfaces available through either transport.
This guide describes the dashboard's public route, store and component
structure.

## Runtime boundary and commands

`src/app/page.tsx` is a minimal Next App Router page. It loads
`src/app/client-runtime.tsx`, which is a client component that dynamically
imports `src/App.tsx` with `ssr: false`. This boundary keeps the browser-only
store, WebSocket, xterm and Electron bridge out of static prerendering. Next
still emits the document, CSS, fonts and JavaScript assets; the existing
client-side registry and in-memory `navigate()` state remain the dashboard's
navigation model. Next does not run in production.

```sh
make dashboard   # Bun install, then Node 22+ and next build (from repo root)
make build       # dashboard export, then the Go binaries
cd web && bun run dev -- --port 3000 --hostname 127.0.0.1
cd web && bun run test       # vitest
cd web && bun run typecheck  # tsc --noEmit
```

`bun run dev` starts `dev-server.mjs`, the development-only Node loopback
server. It defaults to `127.0.0.1:3000`, accepts `--port`/`-p` and
`--hostname`/`-H`, and uses `AETHER_DASHBOARD` as its gateway target. The
target may be a local `aether gui` URL or a server-hosted HTTPS dashboard;
local targets also proxy `/local`, while both targets proxy `/api` and `/ws`
with WebSocket support. The proxy preserves the browser `Host` and `Origin`
headers so the shared gateway remains the WebSocket origin boundary. Other
requests and Next HMR upgrades go to Next's development handler. The Next
Node inspector attach endpoint is unavailable through this server.

For a local gateway, run it and start the dev server in separate terminals.
The local gateway prints a tokened URL; copy its `token` query value onto the
dev URL:

```sh
# terminal 1
aether gui --port 8080 --url

# terminal 2
cd web && AETHER_DASHBOARD=http://127.0.0.1:8080 \
  bun run dev -- --port 3000 --hostname 127.0.0.1
# open http://127.0.0.1:3000/?token=<token-from-aether-gui>
```

The client moves `?token=` into `sessionStorage` under `aether.token`, removes
only that query parameter from the address bar, and sends the value as a
Bearer token on HTTP or a `token` query parameter on WebSockets. The token is
minted per `aether gui` process and stops working when that process exits.
When `AETHER_DASHBOARD` points at the server gateway instead, the browser
sends no token: Tailscale WhoIs identifies the phone or development browser
on every request.

Node 22+ is required for a hand-run dashboard build. The complete contributor
toolchain and the optional desktop installer workflow are in
[CONTRIBUTING.md](../CONTRIBUTING.md#toolchain).

## Testing on a phone

The shipped phone path is the server-hosted gateway: set `web-port`, then open
the server's MagicDNS name on a phone joined to the tailnet
([Testing on a real phone](#testing-on-a-real-phone)). The connection is
HTTPS and carries no browser token; Tailscale WhoIs identifies the phone's
source address on every request. The server-hosted dashboard opens at the
board and does not offer machine-local onboarding, linking or update actions.

For a contributor's loop against a dashboard build that is not embedded in a
server yet, use the development server as a LAN-facing proxy to a local
`aether gui`:

```sh
# terminal 1, on the computer the phone will reach
aether gui --port 8080 --url

# terminal 2, with <lan-ip> that computer's address on the phone's network
cd web && AETHER_DASHBOARD=http://127.0.0.1:8080 \
  bun run dev -- --port 3000 --hostname <lan-ip>
# on the phone: http://<lan-ip>:3000/?token=<token-from-aether-gui>
```

- **Binding the LAN address gives up the loopback boundary for as long as
  the dev server runs.** Every device on that network can reach a proxy in
  front of a gateway with the member's authority on the linked server, and
  the local bearer token is the authentication: it travels in clear over
  HTTP, sits in the URL and stays in the phone's history. Use a network you
  trust, and stop the dev server when the session ends. The shipped boundary
  is the loopback rule in [security.md](security.md#the-dashboard-gateways);
  this is a development-time exception a contributor opts into by hand.
- `--hostname` has to be the address typed on the phone. Next's development
  server permits only localhost and the hostname it was started on; any other
  origin needs `allowedDevOrigins` in `web/next.config.ts`.
- The token is minted per `aether gui` process, and the phone needs that one.
  Nothing else authenticates the local development proxy.
- A tunnel or proxy in front of this must preserve `Host` and `Origin`. One
  that rewrites `Host` breaks every WebSocket - the terminal, the event feed
  and the attach stream - while plain HTTP keeps working, which makes for a
  confusing half-broken dashboard.
- This serves the Next development build over plain HTTP, not the static
  export the binary embeds, and it needs the computer awake and on the same
  network. It is a contributor's loop, not the shipped phone path.

Automated phone coverage is the `mobile` Playwright project, described in
[testing.md](testing.md).

## Build pipeline and the embed

`web/next.config.ts` sets `output: 'export'` and `distDir: 'dist'` outside
development. `next build` therefore writes a static `web/dist` artifact:
HTML, `_next` assets, CSS and copied public files. There is no production Next
server or Node process in the shipped dashboard.

`web/embed.go` embeds `web/dist` with `//go:embed all:dist`, which fails to
compile against an empty directory. The build output is not committed, so the
invariant is kept by two placeholder files:

- `web/dist/.gitkeep` is committed so a clean checkout compiles the Go server
  before anyone has run the web build.
- `web/public/.gitkeep` is copied into `dist` by every Next build, so emptying
  the output directory never breaks the next Go build.

`.gitignore` ignores generated `web/dist` and Next metadata while retaining
`web/dist/.gitkeep`. A binary built without running the web build serves the
gateway's "dashboard not built" response rather than a blank page.

CI installs Bun with `oven-sh/setup-bun` (version pinned in `web/.bun-version`)
in jobs that run `make build` or `make release`, plus a dashboard job that
typechecks and tests the SPA on its own.

## The three extension seams

Later waves (the board, terminal, diff timeline, team surfaces) add views and
state without editing the shell.

**Route registry** (`src/routes/registry.ts`). A route file calls
`registerRoute('board', Board)` at module scope; `src/routes/index.ts` imports
it once for that side effect. The center view looks the current route up by
name and renders it with `route.params`. Navigation is a store action -
`navigate('terminal', { runId })` - rather than a URL router: Next supplies the
document and static assets, while the dashboard remains one client screen and
every surface uses the same action.


**Store slices** (`src/store/`). One Zustand store composed of slice creators,
one file each (`server`, `workspaces`, `runs`, `members`, `terminal`, `board`,
`palette`, `approvals`, `presence`, `cost`, `timeline`, `diff`, `shell`,
`local`, `ui`). A new feature adds a slice file and one spread in
`createRootStore`. Slices are typed against the whole root state, so a slice
may read another's data. Only view preferences (theme, sidebar width and
collapse state, `activeWorkspace`, grouping, dismissed update versions,
terminal zoom) are persisted; `persistedUi` in `store/index.ts` is the list
that decides. Server data is always re-fetched.

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
| `card:chips` | `{ run }` | the wrapping metadata row after the harness, branch and last-commit readout |
| `card:footer` | `{ run }` | the card's bottom row, right of the owner and timestamp |
| `statusbar` | none | the status bar, for refresh, shortcuts and other live contributors |

The `statusbar` Slot is mounted once, even when narrow layouts collapse its
details. The command palette is not a status contributor; it has one
independent host in `AppShell`.

Card slot content may render its own links and buttons; the article's pointer
handler ignores interactive descendants, so those controls stay interactive.
Conflict chips, watcher avatars and approval badges belong in these slots, not
in `run-card.tsx`.

## Sidebar

`src/components/shell/sidebar.tsx` owns the resizable workspace/run sidebar
beside a persistent 48px activity rail. The rail carries existing navigation,
capability gates, accessible labels and tooltips, a 2px active indicator and
overflow when all destinations do not fit. The adjacent sidebar is a preferred
260px wide and remains constrained to 200-520px.

The sidebar is a workspace switcher over a flat list of that workspace's runs.
There is no run tree: one workspace is in view at a time, so the runs group
instead by state or by owning member (`groupBy`, persisted). Rows and headers
are compact rather than a lower navigation card. At 1000px and narrower the
adjacent workspace/run pane collapses into the persistent activity rail, which
exposes **Expand sidebar** without changing the stored preference. The width
handle remains a keyboard and pointer window splitter (see
[Keyboard and focus](#keyboard-and-focus)).

At 640px and narrower the expanded pane is a modal drawer instead: a Radix
`Dialog` over a scrim, dismissed by a tap outside, by Escape, or by any
navigation it makes, with focus trapped inside it while it is open. The
activity rail travels inside the drawer, so a surface is still one tap away
while the run list is up, and the 48px column the drawer leaves behind keeps
the center view from reflowing under the scrim. The route itself is what
closes the drawer, so a run row and a rail link both take the drawer away
without either knowing it exists. There is no splitter in the drawer: the
viewport sizes it, capped at the width of the screen less the rail.

Being a dialog, the drawer also stands the shell's global keys down while it
is open, the same way every other modal does (see
[Keyboard and focus](#keyboard-and-focus)) - the palette, `n` and the `g`
chords are unreachable until it closes. `Mod+B` is the exception: the drawer
answers that one itself, because it is the key that opened it, and a member
who opened the drawer from the keyboard must be able to close it the same
way.

- **The switcher sits above everything it scopes**, and appears only when there
  is a choice: a single workspace renders as a plain label with its base branch
  under it, because a picker with one option is a control that cannot be used.
- **The runs come from `sidebarRuns`/`sidebarGroups`** in
  `src/store/selectors.ts`, filtered to `activeWorkspace` and sorted
  worst-state-first, then most-recently-changed-first, so what needs a human is
  at the top of whichever group it is in. An empty scope shows every run, which
  is what the list falls back to before hydration has named a workspace.
- **The shared `RunList` keeps visible run labels to two lines**, while each row button retains the full label as its `aria-label`.
- **The attention badge counts, it does not navigate.** The runs below are
  already sorted worst-first, so the number is for a scrolled sidebar or a
  stall that landed while the member was elsewhere in the app.
- **The header carries New run and the grouping.** New run opens the launch
  form (gated on `canLaunch`). Grouping is a two-segment control, Status and
  Member, with `aria-pressed` on the current one, so the pressed segment is
  the state and the other one is the action.
- **The activity rail reaches every other view**, with the active route marked
  by `aria-current`. Board and All runs lead it, and scope-wide entries keep
  the method or local-verb capability gates that power their views.
- **The rail and palette share the scope-wide surface list** from
  `src/lib/surfaces.ts`, so a surface cannot be named one thing in navigation
  and another in the palette, and neither can forget its gate. Approvals
  carries the pending count in its accessible name; the status bar keeps its
  own copy for when the member is looking elsewhere.
- **A nav entry is named what the view it opens is titled**, including
  `routes/workspaces/` as "Manage workspaces" rather than "Workspaces".

## Files view

`src/routes/files/` is the Files explorer and editor. It combines each visible
workspace's base branch, live run checkouts, and the authenticated member's
own persistent configuration roots from `config.roots`. `files.tree`,
`files.read`, `files.write`, `files.diff`, and the `config.*` methods are
available through both the local SSH-backed gateway and the server-hosted
WhoIs gateway. Directory requests are lazy and cached in
`src/store/files.ts`; file content comes from `files.read` or `config.read`.
A live run can switch from **File** to **Diff vs base**.

The tree is a browse pane beside the editor at medium widths. On narrow
screens it is the first view; selecting a file opens the editor and **Browse**
returns to the tree. CodeMirror provides syntax highlighting for JSON/JSONC,
JavaScript/TypeScript, Markdown, Python and TOML, plus find/replace. The
editor is bounded to complete UTF-8 text without NUL bytes and 512 KiB;
binary and truncated responses remain read-only.

The action label states the write target: base files show **Commit to
<branch>**, creating one file commit without pushing upstream; live-run files
show **Save**, changing the run's uncommitted checkout; configuration files
show **Save**, changing only the authenticated member's persistent home.
Base writes require **Push**, run writes require **Steer**, and config reads or
writes are always for the calling member and require **Launch**. A new
configuration file accepts nested relative paths and refuses to overwrite an
existing file.

Saves are explicit (**Save**, **Commit to <branch>**, or Ctrl/Cmd-S); there is
no autosave or force-save. Open tabs and dirty drafts live in memory and survive
route changes and reconnects to the same identity. A different authenticated
member or server clears them. The browser warns before unloading dirty buffers.
The revision is the SHA-256 of the complete bytes read. A failed or stale save
keeps the draft and its error. On a conflict, **Reload from server** replaces
the document and discards that draft. **Discard edits** restores the last
successfully loaded or saved content without fetching.
All of the member's run containers and environment terminal mount one shared
read-write persistent HOME, so accepted configuration imports and saves are
visible to already-running processes immediately; a tool may need to reload.

## Title bar

`src/components/shell/title-bar.tsx` renders the browser and Electron
title/command bar at 35px. The command center names the active workspace and
opens the existing command palette through `togglePalette(true)`. The bar pads
itself with `env(safe-area-inset-left/right)` so a landscape notch cannot sit
over it; the inset is 0 on every screen without one, and the Electron traffic
light inset above wins where it applies.

In Electron the window is frameless, so the SPA draws the bar and its native
controls: on Windows and Linux, minimize, maximize/restore and close are wired
to `window.aetherDesktop.controls`; macOS keeps its native traffic lights and
the bar reserves 78px for them. The bar is `-webkit-app-region: drag` and
every button is `no-drag`.

The bridge carries one more thing the SPA cannot do for itself:
`window.aetherDesktop.chooseFolder()` opens the shell's native directory
dialog, parented to the asking window - a sheet on macOS, modal to the window
on Windows; a Linux portal chooser runs out of process and is neither, which
is why the caller disables its button while one is open - and resolves to the
chosen absolute path, or `""` when it was cancelled. A window it cannot
resolve rejects with `no window asked for the folder dialog` instead, so a
caller can say what went wrong. It is optional on the type for the same
reason `shellVersion` exists: a shell built by an older `aether gui build`
does not have it.

`App.tsx` mounts it above the whole app, the `ConnectionError` page included:
that page replaces the workbench, while the titlebar and native controls
remain available in a total failure state. Browser tabs have no Electron
bridge but still show the command center.

On the first desktop launch, `LaunchSplash` covers the window with the
original shooting-star scene, Aether mark and VT323 wordmark. It stays for at
least 600ms, leaves after hydration or failure, and has a 2500ms cap followed
by a 260ms fade. The cap prevents a failed connection from holding the window
controls indefinitely. Browser tabs, reloads in the same window session and
`prefers-reduced-motion: reduce` skip it. See [styles.md](styles.md) for the
scene's motion details.

## Window size and overflow

The shell is a fixed column - title and command bar, update prompts, activity
rail with its adjacent workspace/run sidebar and center view, and a status bar
- and nothing in its own chrome scrolls sideways. The two bars are 35px and
22px for a mouse and grow for a finger; see the tokens below. A control pushed
past an edge is unreachable, not merely off screen, so every row states what
gives way first.

`desktop/main.js` sets `minWidth: 960` and `minHeight: 600`. That is the size
the desktop rules are designed against; a browser tab has no such floor, so
the same rules degrade below it rather than break. A phone is the far end of
that: `src/app/layout.tsx` exports the viewport the shell needs there.

- `width=device-width, initial-scale=1` - the page is laid out at the device's
  own width rather than a desktop-sized canvas scaled down.
- `viewport-fit=cover` - the shell paints under the notch and the home
  indicator, and the chrome that touches those edges pads itself back out with
  `env(safe-area-inset-*)`: the title bar sideways, the status bar and its
  details popup downwards, the sidebar drawer on all three edges it reaches,
  since it is the one surface that spans a screen corner to corner, and the
  row holding the activity rail on the left, which is the edge a landscape
  notch covers when the drawer is not up. A surface that pads itself keeps
  painting to the edge and insets only what it holds, so the notch shows the
  bar's own colour rather than a gap. Every inset is 0 where there is none, so
  nothing guards them. Toasts sit above the status bar rather than against the
  screen edge, so their offset adds the inset to the bar's own height - and it
  has to be given to `sonner` twice, as `offset` and as `mobileOffset`,
  because `sonner` swaps to the second below 600px and otherwise falls back to
  a 16px default that lands inside the bar.
- `interactive-widget=resizes-content` - on a browser that honours it
  (Chrome and the Android WebView; iOS Safari does not), the soft keyboard
  shrinks the layout viewport instead of sliding the page under itself. That
  is what every `dvh` in the app - dialogs, the palette, selects, menus, the
  status popup - is already sized against, so they all shorten when the
  keyboard opens. Nothing in the shell uses `vh`. Where it is ignored the
  layout viewport does not move, which is why dialogs also anchor to the top
  below `sm` (see the end of this section).
- `themeColor` per `prefers-color-scheme` - the browser reads it before the
  SPA has applied the member's stored theme, so it follows the OS scheme
  rather than the app setting.

A layout that only changes size belongs in CSS. The ones that mount
different elements for a finger than for a mouse - the run header's menu, the
diff timeline's disclosure, the activity filter bar - ask `useMediaQuery` in
`src/lib/hooks.ts` instead, and every edge it asks about is a named constant
in the same file: `coarsePointer` is the CSS variant below asked from
JavaScript, and `belowSm` and `belowMd` are Tailwind's own 640px and 768px a
pixel short. New code reads an edge from there rather than writing a query,
so a layout that stacks in CSS and a layout that stacks in JavaScript cannot
disagree about where. Two call sites predate the hook and still hold their
own literals - `shell/sidebar.tsx` and `shell/status-bar.tsx` - and move onto
it in a follow-up.

**Touch density is one variant, defined once.** `src/index.css` declares
`@custom-variant coarse (@media (pointer: coarse))`, and a control that a
finger has to hit carries its touch size beside its desktop one - for example
`size-[22px] coarse:size-11`. It answers for the primary pointer, so a touch
laptop with a trackpad keeps the desktop density. Under it the `Button`
sizes, `CommandItem`, `DropdownMenuItem`, the `CollapsibleTrigger`, the
`Select` trigger and its options, the dialog close, the palette trigger and
input, the status bar controls, the sidebar run rows and the sidebar's own
buttons, the run-list title, the files tree rows and the approvals controls
grow to 40-44px, and the terminal toolbar row grows with the buttons in it.
Desktop density is untouched.
Use this variant rather than a new breakpoint or a per-component pixel value.

**The two bars are tokens, not repeated numbers.** `--title-bar-height` and
`--status-bar-height` are declared in `src/index.css` and redeclared once
under `(pointer: coarse)`, where they become 48px and 44px so a 44px control
fits inside them. A row that has to line up with a bar reads the token - the
title bar and the sidebar's workspace switcher, the status bar with every
control and readout in it, the command palette's drop from under the title
bar - and so does every offset measured from one: the update banner cap and
the toast offset. Add a coarse size to a control in a fixed-height row only
together with the row, or the control grows out of the bar that holds it.

Dialogs anchor to the top (`top-4`) below `sm` and centre from `sm` up. Where
`interactive-widget` is ignored, a centred fixed dialog sits behind the
keyboard; anchored to the top it stays in the visual viewport, and a dialog
taller than the screen is clamped by `max-h-[calc(100dvh-2rem)]` and scrolls
inside itself. `sm` is a width breakpoint, so a desktop window narrower than
640px is treated as a phone here too.

- **Update notices** keep the message, status icon and action hierarchy visible.
  Their actions become a narrow-screen grid and return to a desktop flex row;
  technical output is bounded and expandable, and the dismiss control remains
  keyboard reachable.
- **The status bar** keeps connection state, the theme control and the
  status-slot contributors reachable at every width. Narrow layouts put
  secondary readouts in a bounded, keyboard-reachable popup; wide layouts
  expand them inline. The single status Slot remains mounted while details are
  collapsed - a contributor owns a keyboard shortcut of its own, which is why
  the popup is a `Collapsible` with its own dismissal rather than a Radix
  overlay that unmounts when it closes. It dismisses on Escape and on a
  pointer down anywhere outside it, unless a dialog above it owns the key;
  it takes Escape before the shell's own does, so dismissing the popup on a
  run does not also leave the run (see
  [Keyboard and focus](#keyboard-and-focus)).
  The popup also writes out the facts a pointer reads from a hover: the disk
  breakdown, the protocol version and what this machine is linked to. Tooltips
  and `title` stay hints for a pointer, never the only copy of a fact.
- **The run header** keeps the title, task, branch and harness mode readable with
  compact headers and a wrapping action group. Terminal tabs remain one keyboard
  stop with internal horizontal overflow. On a coarse pointer the action group
  becomes one **Actions** button and every verb moves into its menu, where
  each carries its full label at finger size. Six 44px buttons do not fit
  across a phone, and the mouse answer to a narrow row - 22px icons with the
  label in a hover tooltip - is six unnamed icons to a finger. Hand off is in
  the same menu.
- **The board** uses one column on narrow screens and three columns from the
  `lg`/1024px breakpoint, with compact flat run cards and vertical scrolling on
  small screens. State labels remain visible; empty, loading and error panels
  use the same bounded surface hierarchy.
- **The workspace/run sidebar** collapses at 1000px and narrower into the
  persistent 48px activity rail, which exposes **Expand sidebar** without
  changing the stored preference. At 640px and narrower its expanded pane is a
  modal drawer over the main view, taken away by a tap outside, by Escape or
  by the navigation it makes, rather than a pane that stays over the run it
  just opened.

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
- **Server-gateway reconnects also refresh the snapshot** when replay is
  possible: Tailscale may identify the new connection as a different member.
  Only a local gateway's fixed identity can reuse replay without that refresh.
- **A failed hydration retries** on the same backoff, and the affected panes
  say the server is unreachable rather than animating skeletons forever. That
  generic copy never overwrites a more precise error already recorded.
- **A total failure replaces the shell with one error page.** When nothing has
  hydrated and an error is recorded, `ConnectionError` takes the window
  instead of an empty sidebar and an empty board behind a toast. Which hop
  failed picks the copy: `network` says this computer is offline, `server`
  says the server did not answer through the selected transport, `gateway`
  says the local `aether gui` process stopped answering, and an access refusal
  preserves the gateway's own reason. The gateway's message appears in an
  initially open "Technical details" disclosure, and the page suppresses the
  toast that would otherwise repeat it. Retry clears connection state and
  remounts the subscribe-and-hydrate cycle rather than reloading the page.
- **A local token refusal is reported as access failure, not an unreachable
  server.** `connect` reads `GET /api/v1/capabilities` before it opens the
  stream, and a `401` there means the local gateway refused its token. The
  store records that refusal verbatim, stops retrying and tells the member to
  open a fresh URL from `aether gui`. The token is minted per process and held
  in the tab's session storage, so a bookmarked URL, a second tab or a
  restarted `aether gui` needs a newly printed URL. On the server gateway
  there is no token check: WhoIs identifies the source address on every
  request, while a tagged node is denied and an unavailable identity service
  reports its own `403` or `503` refusal.
- **The sockets reopen on a foreground or network return.** Both
  `connectEvents` and `connectAttach` subscribe to `visibilitychange`
  (visible) and `online` through `onWake` in `src/lib/stream.ts`. A phone
  freezes a background tab's timers and drops its sockets, so a tab coming
  back from the pocket would otherwise sit out the remainder of a wait that
  caps at 30 seconds. Either event clears the pending retry timer and resets
  the backoff unconditionally - a tab that was away cannot know how long the
  failure lasted - and reopens when there is no socket. The two differ on a
  socket that is still there: a foreground return leaves it, since tearing a
  working subscription down would replay the log for nothing and the tab
  being hidden said nothing about the network. `online` did, so it replaces
  the socket whatever state it reached, an acknowledged one included. A
  wifi-to-cellular switch leaves exactly that socket half open: the browser
  goes on reporting it as connected, the store goes on saying Live, and no
  close ever arrives, because the server's end sees the FIN and the phone
  does not. `online` is rare, so one resubscribe from `lastSeq` and one
  re-attach with its replay are the cheaper mistake. The 30 second cap stays
  for genuine outages, and
  an attach the gateway refused - or one parked on a `session ended` close,
  whose transcript cannot change again - is an answer rather than a failure,
  so neither event re-asks it.
- **The hydration retry wakes as well.** A re-hydration that fails after the
  first good one leaves a cursor to replay from, so the reopened stream goes
  live without re-fetching and nothing else would restart that timer. A wake
  with a retry pending clears it, resets its backoff and re-fetches at once;
  a wake with none re-fetches nothing.
- A `run.status` event for a run the client has never seen fetches that run
  before the event is applied, which is what keeps two quick transitions of a
  brand new run in order. If the fetch fails the event is unresolved: the
  cursor stays put and a fresh snapshot is taken to repair the store. A
  `run.deleted` event removes the run from every connected dashboard; a
  `run.status` event that raced a local deletion treats a `404` re-fetch as
  resolved instead of forcing a redundant hydration. An event naming an
  unknown workspace re-fetches `workspace.list`, because workspaces arrive
  only by fetch and a run under an unknown one would render nowhere. An event
  whose actor is not in the members map re-fetches `member.list` for the
  same reason: no `member.*` event exists, so a teammate who joined after
  hydration would otherwise render as a raw ID forever. A
  `workspace.timeline` entry of kind `handoff` re-reads its run the same way,
  because a handoff publishes no `run.status` event to carry the new owner.
  A `server.update` event lands in the `server` slice, which feeds the update
  prompts.

**The capabilities descriptor is the transport seam.** The store holds the
`GET /api/v1/capabilities` answer (`gateway`, `methods`, `ws`, and `local`
where the gateway has local verbs - the server gateway omits it), and
`useCapability()` in `src/store/hooks.ts` wraps it as three predicates -
`hasMethod`, `hasLocal`, `hasWS` - with `methods: ["*"]` meaning everything
and a missing `local` meaning no local verb is available.
When the descriptor is `null` (a gateway that predates the endpoint), the
fallback is the read-and-steer method set every gateway has always served,
`events` and `attach` sockets, and no local verbs, so an unknown gateway
degrades to monitoring rather than to "everything". Views gate on these
predicates rather than sniffing the URL, which is what lets the same SPA
render against a gateway with or without the local surfaces
(`docs/local-gateway.md`).

**Capability is half the gate; the caller's role is the other half.**
Transport capability answers what the gateway can carry, not what this member
may do. `useSelfRole()` and `useIsAdmin()` in the same hooks file read the
role off `server.info`'s member record, and every admin affordance needs both
predicates: the gateway can carry the method *and* the caller holds the admin
role. Reads are gated on capability only, so the roster is reachable on the
server-hosted dashboard as well as through `aether gui`; the server remains
the authority and checks every call again. A non-admin cannot edit membership
or roles but can grant or revoke access to their own agent account.

Every request goes through `src/lib/api.ts` - the only module that knows route
shapes, gateway authentication and error decoding. It carries exactly the
methods the views call; the team-feature methods arrive with the tickets that
use them. Every call is a `POST /api/v1/<method>` bar three `GET`s - the diff
tab's patch text, the status bar's disk number, and the capabilities probe -
because those read a working tree, a filesystem, and the gateway descriptor
rather than RPC methods. `aether gui` sends its per-process token as
`Authorization: Bearer` on HTTP and as `?token=` on WebSockets. The
server-hosted gateway sends no token; WhoIs authenticates each request.

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

## Board

`src/routes/board/` is the default center view: the active workspace's run
cards in the three buckets the GUI spec copies from Orca. `needs-attention` is
Needs You - the agent is waiting for you, or the run stalled, and the reason
strip on the card says which - `queued`/`provisioning`/`running` is Working,
and `completed` plus the final statuses are Done. An active run whose approval
request is still pending also presents as needs-attention on the board and in
the sidebar - the pause is invisible in the domain status, so `runState` takes
a pending flag fed from the approval inbox. Cards sort by last state change,
newest first.

The board header wraps its title, run count Chip, descriptive copy and toolbar.
Its grid is one column on narrow screens and three columns from the
`lg`/1024px breakpoint, with flat bordered columns, readable state headers and
bounded card content. The primary run-card button contains the state and
title/task metadata as bounded previews, each capped at three lines; the button
keeps the full `runLabel` as its accessible name, and the shared `RunHeader`
exposes the full task through **View full task**. An empty workspace
shows one "Ready for a task" panel and a primary New run action rather than
three repeated empty columns. Loading uses delayed skeletons, and hydrated
empty buckets say "Nothing here." without confusing an in-flight request with
an empty result.
The card's article remains a pointer surface for noninteractive metadata, while
interactive descendants and any non-collapsed text selection are ignored by
the article handler. Branch text is explicitly navigation-exempt so it can be
selected or copied without opening the run. The branch metadata row shows the
full branch in its `title` and has a copy button beside it.
Reaching for the branch is therefore not a way into the run; the rest of the
card is. Copying goes through `src/lib/clipboard.ts`, shared with
`CopyableCommand`, because an origin without `navigator.clipboard` - plain
http, an older engine - has to fall back to selecting the text for a manual
copy rather than failing quietly.

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

**The Needs you reason survives a fetch.** `protocol.Run` carries `reason` -
the last `run.status` reason, persisted with the run and sanitized
server-side - so a run that was already in needs-attention when the tab
loaded still says why: `waiting for your input` and its siblings when the
agent reported it, `stalled: ...` when the silence heuristic parked it.
`toRecord` in `src/store/runs.ts` prefers the wire reason and falls back to
the previously stored one only when the fetch omits it and the status has
not changed (a legacy gateway); a live `run.status` event still overwrites
it with the event payload's reason. An approval pause keeps its fallback: a
card with an empty reason uses its oldest pending request's action as the
summary.

**The paused badge hydrates from the same snapshot.** With `paused` on the
wire (above), a reload shows the badge for a run paused earlier, and the
palette offers the right one of pause/resume. Against a legacy gateway
whose runs carry no `paused` field the state stays unknown until a live
`workspace.timeline` pause or resume arrives, and neither surface offers a
verb rather than offering the one the server would refuse.

## Commands: one list, two ways to reach it

`src/lib/commands.ts` holds every verb the dashboard can perform - the run
verbs (pause/resume, send a message to the agent, close as merged or abandoned
at any stage that holds a record, kill, delete, protect/unprotect, relaunch,
pull branch, hand off) and the board verbs (open the board or the list, launch,
launch from a template, mark all seen) - as data: an id, a label, an icon, the
capability gate, and the call itself. `useCommandRunner()` performs one and
reports the outcome the same way everywhere: gateway verbs toast their
past-tense name or the server's refusal verbatim. Deleting a run also removes
it from the local run map after the server confirms deletion.
- **The command palette** (`src/components/palette/`) is the cmdk palette:
  `⌘K` on macOS and `Ctrl+K` elsewhere, anywhere in the app (see [Keyboard and
  focus](#keyboard-and-focus)), or the command center in the titlebar. Both
  entry points use the existing toggle action. The palette is mounted exactly
  once by `AppShell`, independently of the status Slot, and its quick input is
  top-centered directly under the 35px titlebar, max 600px, with compact
  bounded rows before the dialog portals to the document.
  It jumps to runs and workspaces - opening a workspace also makes it the
  active scope, so the sidebar and the board follow - and steers **the run the
  center view is showing**, any run-detail tab, since it keys on
  `route.params.runId` rather than on a route name. From the board there is
  none, so reveal a run first. Its "Go to" group is `src/lib/surfaces.ts`, the
  same gated list the activity rail renders.
- **Visible buttons**, so nothing important is reachable only by a shortcut:
  New run in the sidebar header, in the board header and in the notice an
  empty board shows in place of its columns; every view in the sidebar nav;
  and the run action bar (`src/components/run-actions.tsx`) in the header of
  every run-detail tab, which is where the run verbs live for a member who has
  not learned `⌘K` yet.

Two things the buttons add. A `Command` carrying a `confirm` field - kill,
delete and both close actions - opens a dialog naming the run before it runs;
the palette does not ask, because a palette item is already several
deliberate steps (open, type, select) away from an accident, where a button
is one click. And the bar locks while a verb is in flight, showing a spinner
on the one running, or on the **More** trigger while any verb is in flight: a pull shells out to `git fetch` over SSH and takes seconds, and a
second click would race the first for the same ref. Buttons
also take the command's `short` label and keep the full sentence as their
tooltip, because the action bar is intentionally compact. The overflow menu
has the room, so it prints the whole label instead.

hand off and protect need the run's owner or an admin. Before hydration the
caller's own record has not arrived, and the mirror answers yes rather than
making the shell's buttons appear a beat late. Pull is the exception that is
not a question for this policy at all: it is the desktop gateway fetching a
published run branch into the repository on this machine, so it answers to
`hasLocal('pull')` alone. It does not refresh a workspace base or authorize a
mirror source.

### The forms

The three verbs that need prose open a dialog rather than calling straight
through: launch, send a message to the agent, and launch from a template. The
message form is `inject-dialog.tsx` over `run.inject` - the wire name stays
`inject`, only the words the member reads changed. The launch and message
forms are a store dialog (`openPaletteDialog` on the `palette` slice) hosted
by `AppShell` through `components/palette/dialogs.tsx`, so a button on any
surface opens one by asking the store, with no dependence on the palette or
the status bar being on screen. The template form's open state lives with
`CommandPalette` in `index.tsx` instead, because the store's dialog union
knows only the other two. It lists the active
workspace's templates over `template.list` and starts the run with
`template.launch` (both on `lib/api.ts` like every other call), then reveals
it.

The launch form asks for an account, a task, an agent and a mode. `account.list`
puts the caller first, followed by accounts explicitly shared with them. A
shared selection makes `agent.list` return that account's custom definitions
and sends its ID as `account_member_id` on `run.launch`. The task is optional in
interactive mode - a taskless launch drops the member into the agent's TUI
with no seeded prompt - and required in headless, which has no interactive
surface, so the form disables Launch and says why rather than sending a
request the gateway will refuse (`runLaunch` in `internal/sshd/handlers.go` is
the same rule). Only what was actually chosen goes on the wire: an empty task
and the default `tui` mode are the server's own defaults. The **Agent** field
is always there. It reads "Choose an agent" until one is picked, and there is
no way back to that state once one is. Under it are the installed entries from
`agent.list`, then `custom`, the escape hatch that `agent.list` never returns
and that only launches where the deployment pinned a harness with
`--harness-definitions`. `agent.list` reports installation from the
selected account's persistent `~/.local/bin`; uninstalled shipped entries
remain visible on the Agents page so setup can install them. The launch form
also remembers the most recently used installed agent for each account and
falls back to the first installed entry. With nothing installed nothing is
preselected, so Launch stays disabled until the member picks one: "No agent is
installed in this account." and a **Set up an agent** button sit beside the
field rather than replacing it, and the button opens the Agents view. A failed
list request shows its error and no setup button - nothing here can fix a
gateway that did not answer - and Launch stays disabled there too. **Refresh
agents** retries discovery after a connection failure or an installation
completed in another terminal.

Neither launch form asks which workspace to launch into: both take
`activeWorkspace` and say where the run will land, naming the workspace and its
base branch. The switcher is the picker, so a second one inside the dialog
would be a place for the two to disagree.

Launching is gated on `run.launch` **and** on the launch permission
(`canLaunch`). The local gateway advertises every method regardless of who is
behind it, so capability alone would put the button in front of someone the
server would refuse.

Base capture is server-side and precedes row creation. A mirror or local-base
failure is reported by Launch and leaves no run row; the dashboard does not run
a client-side base refresh before trying again.

## Keyboard and focus

Everything the dashboard can do is reachable without a mouse, and every control
a keyboard reaches draws the same focus indicator.

**One focus indicator.** `focusRing` in `src/lib/utils.ts` is the shared
outline utility. Every focusable primitive composes it, including
`SelectTrigger`, `SelectItem`, `Checkbox` and `CollapsibleTrigger`. Raw
buttons, links, menu items, dialog close controls and resize handles use it
too. Controls that fill a scroll container use the inset variant so the
outline is not clipped. Keyboard outlines appear immediately, without a
colour transition, and focus remains on a real control when a row or pane
changes. Verify focus, keyboard and accessibility as observable behavior, not
CSS class or source-string contracts.

It is an outline rather than a ring, for two reasons. Windows High Contrast
(`forced-colors: active`) discards box shadows, which is what Tailwind's
`ring-*` compiles to, and would leave the app with no focus indicator at all.
And `ring-*` already means "selected" on the member colour swatches, where a
focus ring in the same property could not be told apart from the selection.

The outline sits 2px outside the control, except where the control has no
room outside it: a row that fills its scroll container - a sidebar run, a run
list entry, a diff snapshot, a board card - and a menu item, which sits flush
against its neighbours. There it is drawn 2px inside instead. An outline
outside a full-bleed row is clipped at both edges by the scroller, and padding
the container would inset the dividers that are meant to run edge to edge. The
inset has to carry the same variant as the token
(`focus-visible:-outline-offset-2`): a bare `-outline-offset-2` is one
pseudo-class less specific and loses at the moment the outline is drawn.

`DropdownMenuContent` and `DialogContent` suppress the outline on themselves:
each takes focus programmatically when it opens and has nothing to show for
it. Their contents are not the same case. A `DropdownMenuItem` takes real DOM
focus under Radix's roving tabindex, so it wears the outline like any other
control, keeping its `focus:` background as well. A `SelectItem` is that case
again: Radix moves DOM focus onto the highlighted option, so it wears the
outline inset like a menu item, and `SelectContent` suppresses its own for the
reason the other two containers do. A `CommandItem` suppresses the outline
too, and that one is deliberate: cmdk never moves focus to it at all, leaving
it on the input and tracking the highlighted row with `aria-activedescendant`,
so a background is all it has, and all it needs. It is the one row the focus
sweep is told to skip.

**The shell's own keys**, listed in the shortcuts dialog behind the `?`
trigger in the status bar. `⌘K` lives with the palette in
`components/palette/index.tsx`, `Shift+/` with the dialog in
`components/shortcuts/index.tsx`, and the rest in
`components/shell/nav-shortcuts.ts`:

| Key | What it does |
| --- | --- |
| `⌘K` / `Ctrl+K` | Open the command palette |
| `⌘Shift+P` / `Ctrl+Shift+P` | Open the command palette |
| `n` | Launch a run |
| `g` then `b` | Go to the board |
| `g` then `l` | Go to all runs |
| `Esc` | Leave a run for the board |

`n` is offered, on both surfaces, only to a member who may launch. The
single-key ones carry no modifier, so `keyboardBusy` in `src/lib/keys.ts`
stands them down whenever something else has the keyboard: a text field or a
select, a terminal, an open menu or list box, or an open dialog. A stray `n`
typed at an agent has to reach the agent, `n` in a menu is that menu's own
typeahead, and `n` on a select jumps to the option that starts with it - the
guard finds a select by its `combobox` role, since the control is a button.
The `g` prefix waits 1.5s for the key that completes it, and any key that goes
somewhere else ends the wait.

The modified palette shortcuts are the exception, and have to be: they cannot
be mistaken for typing, and with the terminal holding the focus and swallowing
Tab they are the way out of a run. They stand down for a modal rather than for
anything that has the keyboard, through `inModal` and the store flag that names
the form the shell is hosting. They take the key from the browser either way,
so a stand-down cannot land the reader in the address bar.

That guard reads the event target rather than the document. Radix dismisses an
overlay from a capturing document listener without stopping the event, and
React commits the close in a microtask that runs before a window listener is
reached: asking the DOM what is open would find the dialog already gone and
let Escape both close the dialog and leave the run. The shell also stands down
on `defaultPrevented`, which is how Radix marks the Escape it just acted on.
The document is asked in one case only, by both guards: a key that landed on
`body`, where focus falls when an overlay removes the control that was holding
it, or merely disables it - Radix's focus scope watches for removals and does
not take the keyboard back from a button that disabled itself mid-flight.
There is no target left to read, so the fallback asks whether a dialog or a
menu is open, and a dialog playing its exit animation does not count. It does
not ask about terminals: focus on `body` with a terminal on screen is
ordinary, and the run dock hands the keyboard to whichever body replaces its
terminal - the refusal, the "Open shell" button, the unavailable notice -
rather than orphaning it.

An open tooltip is the one overlay that neither guard names, and it does not
need to: a tooltip owns no keys, and its trigger is an ordinary control.
Escape is the exception. React Aria dismisses a tooltip from a capturing
document listener that stops the event rather than marking it, so the shell
never hears that press at all: on a run, the first Escape closes the tooltip
and the second leaves. Every other key reaches the shell as usual, and a
tooltip closes on the first of them whatever it is, so a pending `g` is
untouched.

The status bar's details popup is the other overlay outside Radix, and it
dismisses itself, so it has to do by hand what Radix does for a dialog: its
Escape listener captures, and marks the key handled. The shell's own Escape
is a window listener registered when the workbench mounted, long before the
popup opened, so in the bubble phase it would run first and leave the run.
Capturing is what makes Escape dismiss the topmost thing and only that; an
open dialog still wins, through the same `inModal` target guard.

Blocking a control with `aria-disabled` rather than `disabled` keeps it in the
tab order, which is the point; the Styleguide rule below says why. The run
action bar, its overflow trigger, the terminal toolbar and the diff snapshot
list all keep their tab stops while their verbs are unavailable, and each
guards its own handler rather than relying on the browser.

**The modifier is named after the reader's keyboard.** The palette shortcuts
and terminal zoom keys accept Ctrl and Meta alike, because one keyboard sends
one and the other sends the other; only the printed label has to pick a side,
and `shortcutLabel` in `src/lib/platform.ts` picks it from the platform. The
palette badges read `⌘K` and `⌘Shift+P` on macOS, or `Ctrl+K` and
`Ctrl+Shift+P` elsewhere. Terminal copy, paste and find are Ctrl on every
platform, because that is what xterm binds; see [terminal.md](terminal.md).

**Tab strips behave as tab lists.** The run-detail strip (`tabs.tsx`) and both
docks (`components/dock.tsx`) carry `role="tablist"`, `aria-selected`, a
single tab stop that follows focus, and Left/Right/Home/End through
`onTabListKeyDown` in `src/lib/keys.ts`. A removable dock tab advertises
unmodified Delete and Backspace through `aria-keyshortcuts`.

Neither strip is a Radix `Tabs`, though the library is already a dependency.
The run strip cannot be: its four tabs are separate registry routes with no
common parent to hold a `Tabs.Root`, and Radix would emit `aria-controls`
pointing at panels that are not in the tree. The dock keeps each tab's close
affordance inside its native tab button instead of nesting another button
inside the tab list; pointer closing stops that tab's activation. What is left
of the pattern either way is `onTabListKeyDown`, one function both strips share.

Those keys move focus and nothing else. Selection does not follow focus here,
which the ARIA tab list pattern reserves for panels that are cheap to swap:
behind these tabs are a websocket attach, a patch fetch and an xterm host that
replays a transcript, so arrowing from Overview to Events must not open the tab
it lands on. Enter or Space opens the focused tab, a click opens the tab it
landed on, and both work because every tab is a real `<button>`.
Delete or Backspace closes the focused removable dock tab. A close repairs
focus to the next surviving tab, the previous one when closing the last tab, or
the Add terminal tab when the dock becomes empty.
Each run route names the body under the strip as its `tabpanel`, through
`runTabPanel` in `tabs.tsx`, and in both strips the selected tab is the only
one carrying `aria-controls`: on the run strip only one of the four routes is
mounted at a time, so the other three would be naming a panel that is not in
the tree. An open dock's
body is the panel its selected tab names; a shut dock has no body, and a dock
holding no tabs is no tab list at all, so neither names anything.

Opening a run tab swaps the whole center view, which unmounts the strip that
was activated, so the strip the next route draws takes the focus back -
otherwise every tab press would drop a keyboard reader on `body`. It is armed
from the activation rather than the key press, and only from one the keyboard
produced, which carries no click count: a press that is cancelled arms
nothing, and a pointer click leaves focus where the pointer put it. The dock's
strip needs none of this, because it is not unmounted by its own tabs.

**Resize handles are window splitters.** The sidebar's and the docks'
`separator` handles take Tab, name the pane they size with `aria-controls`,
report `aria-valuenow` against their available bounds, move 16px per arrow
press, snap to those bounds on Home and End, and collapse the pane on Enter,
handing focus to the control that restores it. Pointer dragging stays within
the available space and follows the same bounds. Both handles set
`touch-action: none`, without which the browser claims the drag as a pan and
cancels the pointer stream the drag listens to. The sidebar's is a drag a
finger makes: it grows to a 24px hit area centred on its edge under
`coarse:`, without changing what it paints, and carries a `z-index` so the
half of it that overhangs the pane beside it is not covered by that pane.
The docks' is not - dragging a horizontal edge to size a terminal on a phone
is not something a finger does well - so that handle is not drawn on a
coarse pointer at all and the dock offers collapsed, half and full instead
(see [Terminal view](#terminal-view)).

## Terminal view

`src/routes/terminal/` is the run-detail Terminal tab: xterm.js over
`/ws/attach/<run>` (`docs/local-gateway.md`). The run-detail routes share one
tab strip (`tabs.tsx`), so Overview, Terminal, Diff and Events are registry
routes on the same `runId`; the strip is a real tab list, arrow keys included
(see [Keyboard and focus](#keyboard-and-focus)).

Every way into a run navigates to `terminal`, because that is where the agent
is: board card, sidebar row, run list, palette, feed entry, approval,
conflict chip, template, and the launch and onboarding forms. Overview stays a
tab for the metadata a finished run is read for, which `src/routes/run.tsx`
renders as one list: the reason, owner, agent account where the run borrowed
one, the created and changed times, and the last commit. The shared
`RunHeader` owns the run title, task, state, harness and mode above every tab;
each caller supplies the branch as its subtitle. All four tabs render one
`RunHeader` (`src/components/run-header.tsx`), so the run's own state travels
with the reader, and `isRunRoute` in `tabs.tsx` is what keeps a sidebar row lit
while they move between the tabs.
`RunHeader` keeps long task text behind a **View full task** disclosure, so its
compact summary never discards the task.

A run id none of the four tabs can find renders one shared `MissingRun`
(`src/components/missing-run.tsx`) instead of that header, its tab strip and
four copies of a sentence with nothing to press. What it says is what the
store actually knows. While the server is unreachable it reports that, in
the same words `run-list.tsx` uses and with the same split - a dead token is
not a server that is retrying, so that one says what the error recorded -
rather than a claim about the run. While the store is still hydrating it is
a skeleton, held behind `useDelayed` so a fast load never flashes one. Only
once the store has hydrated does it say the run is not on the server, and
offer **Back to board**. The launch paths seed the run they just started -
both template launches and the onboarding first-run form, the way
`launch-dialog.tsx` does - so a launch never lands on the deleted claim.

The center view renders the run-detail route without a key, so one
`TerminalView` is reused across a run switch. The Terminal tab therefore
clears the pane when the run id changes rather than waiting for the attach
ack: a run that ended with no recorded terminal is refused and never acks,
which would leave the previous run's output on screen under the new run's
name.

The Terminal view is a vertical split. The agent terminal keeps flexible space
above a `RunDock` below it. The shared run header keeps the title, task, branch,
harness mode and status readable while its actions wrap at narrow widths. The
terminal status toolbar also wraps without truncating real gateway errors.

The dock header uses a `min-h-9` strip rather than a fixed 40px height. It can
wrap actions below the tabs on narrow screens, while the tab list scrolls
horizontally. Add and collapse controls stay keyboard and pointer reachable;
the close affordance is pointer reachable inside each removable tab, and its
Delete/Backspace shortcut is available while that tab has focus. The splitter
is reachable too. The shell tab strip is a custom manual tab list with one
keyboard stop and overflow scrolling; it does not use a component-level tab
primitive.

`TerminalPane` keeps xterm's host geometry intact while layering the shared
toolbar and Find over it. `TerminalTools` in the same module owns the search,
zoom/reset, copy, copy-last-screen, paste, and `TerminalImageAction` controls;
it delegates terminal key behavior to the xterm controller and clipboard
helpers rather than putting those actions in each dock. `useTerminalImage`
owns the hidden file input, preview dialog, validation, upload call, and
shell-quoted path insertion. Its identity (`terminal`, target, active-tab
key, and enabled state) plus a generation token rejects a chooser, paste, or
upload callback that completes after the host or target has changed. The
find overlay sizes to the available width, so a narrow pane clips neither
its input nor its close control.

The image half of clipboard handling is registered by `useTerminalImage` with
`registerClipboardImages` on the current xterm input in capture phase. The
effect cleans up and registers again when the terminal identity changes; it
claims only image-file paste events and leaves text to the native xterm path.
`clipboardKeys` handles copy and deliberately leaves native paste alive, while
`xterm-host.tsx` composes it with zoom and find in xterm's one custom key
handler.

**The terminal on a phone.** The PTY is the per-dimension minimum over the
clients that impose a geometry on it, so a client that fits xterm to its own
pane reflows the agent's screen for everyone else. The protocol's answer is
the `follow` flag (`docs/local-gateway.md`): a follower renders the session
at the size it already is, is left out of that minimum whether or not it can
write, and is told the size in the ack and in a `geometry` frame after every
change. On `phoneScreen` - a coarse pointer on a screen narrower than `sm`,
from `src/lib/hooks.ts` - all three terminals follow: the run's terminal, its
shell tabs and the environment dock, each of which is a session someone else
may be watching at a desktop's width.

A following terminal therefore:

- sends `follow` in the attach header and renders at the `cols` and `rows`
  the server reports, through `useXterm`'s `size` option, which replaces the
  fit addon and reports no resize. The geometry beside the flag is the run
  terminal's `standardGeometry` (80x24) and the docks' own xterm size, which
  on a phone is that same fixed size; either way it decides nothing but the
  size of a session being created, a new shell tab. The pane gets
  `overflow-x-auto` and pans over a grid bigger than the screen; naming one
  axis is enough, since CSS makes the other a scroller too. This holds while
  steering: the flag, not silence, is what keeps the session unchanged.
- does not steer on entry even on the member's own run. `Take control` is the
  only way in, and `disableStdin` holds until the ack grants write - that is
  what makes xterm's textarea read-only, so a tap on a mirror raises no
  keyboard.

`TerminalKeys` is shown under the host on any coarse pointer while the
terminal is writable, a tablet included: Ctrl, Esc, Tab, the arrows, Enter
and Ctrl+C, each through `terminal.input` so the replay gate and
`disableStdin` treat a tap exactly like a keystroke. Ctrl is a one-shot
modifier held in the host (`armCtrl`), because a soft keyboard sends
characters and never a modifier: it rewrites the next character into its
control code, and a key it has no code for keeps the modifier armed rather
than spending it on the wrong byte. The two copy actions carry a visible
word beside them under `coarse:`, because a tooltip is the only other thing
telling them apart and hover is what opens one.

The pane scrolls the cursor into view whenever it moves or the host takes
focus, but only at a fixed size, where the row being written on can be
outside the pane.

The dock has a persisted height
(`UiSlice.runDockHeight`, default 240px), a collapse toggle, and, once
expanded, a resizer on a fine pointer. A finger cannot drag an edge, so under
`coarse` the separator is not rendered at all and an expanded dock gets a
second header control instead, toggling between half of the room it has and
all of it; collapsed, half and full are the three states touch has. Half is
measured from the dock's own maximum rather than stored, so it follows the
screen. Both docks start collapsed
(`initialRunShellDock`, `initialEnvTerminal`), so the terminal a member came
for owns the window until they ask for a shell. Neither flag is persisted, so a
reload starts collapsed again, and the run dock's is per run because
`shellDocks` is keyed by run id. The header strip stays live while a dock is
shut, so its tab controls expand it: a tab whose dock is collapsed mounts no
xterm host and would never attach. Expanded-only dock actions are omitted while
the dock is collapsed. `TerminalDock` mounted with `openOnMount` expands itself
once, because the Agents and GitHub steps type into it.

Run-shell tab state and its socket registry live in `src/store/terminal.ts`;
the environment dock has the corresponding state and socket registry in
`src/store/env-terminal.ts`. The live attachment objects stay outside
persisted Zustand state, but a dock socket can outlive the component that last
displayed it. When a new xterm host adopts one, the dock calls
`Attachment.rebind()` with fresh callbacks and reopens it when needed. Run
shell callbacks guard the current `{ runID, tab }`; the environment dock
guards its current tab. Host subscriptions are removed on cleanup, while
closing a tab or an exited shell unregisters its socket. Thus route changes and
tab remounts cannot deliver late output, resizes, or image actions to a
disposed host; only the selected shell tab mounts an xterm host and transcript
replay restores its content. The primary agent attach is owned by
`TerminalView` and closes with that route, unlike the run-shell attachments.

The board's `TerminalDock` exposes **Save environment** while the member's
terminal is running. Stopping the container and discarding the saved image
are different decisions, so they are separate actions with separate
confirmations: **Stop environment** is the primary action in the dock and
confirms with a plain primary button, saying the saved image is kept, while
**Reset to standard** appears only when there is a `saved_image` to throw
away, is an outline action, and confirms with the image's own name and
carries the only destructive button either dialog has. That confirmation
names the container only while there is one to stop, because Reset outlives
it. Stopping therefore carries the rest of the status forward rather than
replacing it, so the image survives the container in what the dock knows as
well as on the server, and Reset stays on offer with the environment
stopped. A stop or reset that fails renders the server's error inside the
dialog that caused it. When the terminal is running and `saved_image` is
empty, it shows the hint **Installs here reach agents after you save.** From
the moment a tab opens until its attach is acked, a spinner covers the
terminal, because the xterm host is blank until then. The words follow what
the dock knows: a terminal it has not seen running is **Starting your
environment container**, which is the wait Docker's container start accounts
for; a second tab, a tab switch or an expanded dock is **Connecting to your
environment**, with no container to start. A refused or failed start
replaces the terminal with the gateway's own error instead.

- **The socket is `attach.ts`**, framework-free and the only part with logic
  worth testing. It reuses `backoff()` from `src/lib/stream.ts`, so the
  terminal and event stream reconnect on the same jittered schedule, and it
  splits large input (a paste) into several ordered frames under the gateway's
  64 KiB frame cap, never splitting a surrogate pair. Its `Attachment`
  interface also supports `rebind()` and `reopen()`: a persistent dock socket
  can keep its transport while a newly mounted host supplies current
  callbacks.

- **Steer on entry.** The agent header requests `write` on the first attach
  and the active button carries a short pulse animation; the toggle
  reattaches rather than upgrading in place. Until the member has taken
  control once (`UiSlice.terminalControlTaken`, persisted, and set only when
  the member asked for control and the attach ack granted it, so neither a
  refused request nor an owner's automatic steer silences the hint) a live
  run they are only watching says **Read-only mirror. Take control to type
  into the agent.** A run that can be steered at all - running, or stalled
  on a question - says nothing there, and neither does one whose container
  is still starting, because the spinner already speaks for it. Anything
  else says **This run is not running** beside the disabled control, as
  visible text for the same reason the dock's tab ceiling is; a run whose
  container is still starting says nothing there, because the spinner below
  already does. Whether the member may steer is the server's answer, never
  the client's guess: a `-32001` refusal drops the request back to a mirror
  and disables the toggle. A finished run attaches as a read-only replay of
  its recorded transcript, which ends with a 1000 close, reason `session
  ended` - the signal to stop reconnecting rather than loop replay -> EOF ->
  replay. Every other refusal (unknown run) stops the reconnect loop.
- **A missing session is not a dead terminal.** `-32004` means the run has
  no PTY session, and `internal/sshd/attach.go` refuses rather than waits
  for one, so the client is what has to tell a container that is still
  starting apart from a terminal that is gone. A `queued` or `provisioning`
  run is not attached to at all: the tab clears the pane and covers it with
  `TerminalSpinner`, the overlay the environment dock uses, reading
  **Starting the run's container**, and reports no connection state while
  nothing is connecting. The attach effect already re-runs on `run.status`,
  so the run turning `running` attaches on its own. On a run that is up, a
  `-32004` is retried a bounded number of times on the usual `backoff()`
  before it counts as final, because the server names that case transient -
  recovery starts a session under a row that already reads `running` - and
  says the client's retry is what resolves it. Only then does the tab show
  the gateway's error.
- **A dead end gets no Retry.** Retry is offered on every refusal except on
  a run that has finished, where no session will ever answer it.
  `endedStatuses` is that set, and it mirrors `replayableStatus` in
  internal/sshd/attach.go, which is `domain.RunStatus.Terminal()`: the same
  answer decides whether a missing session is worth waiting out, so the
  client cannot drift from the server gate. A run waiting for a human is not
  finished, so it keeps both its retry and the gateway's own words. A run
  that never started has no transcript either, and its `run.reason` carries
  the provisioning failure, so the tab shows that reason rather than sending
  the reader to the Overview tab for it. That substitution is keyed on the
  refusal's own code: only a missing session says anything about the run, so
  a revoked token or a withdrawn membership still shows the gateway's own
  message.
- **Run-shell tabs always write.** The `+` control opens names `t1`, `t2`,
  `t3`, and `t4`; four is the per-run limit, six is the environment dock's,
  and `Dock` writes whichever `maxTabs` it was given beside the disabled
  control - a disabled button shows no tooltip. Each shell attach uses
  `/ws/attach/<run>?shell=<tab>`, requires write/steer permission, and closes
  its socket when the tab is closed. `RunDock` exposes an uploaded-image path
  only while its attached identity still matches the current `{ runID, tab }`.
  A `-32001` response does not reconnect; the dock replaces the terminal with
  the sentence **You can view this run but not open a shell in it**. A normal
  `1000` socket close removes the finished tab.

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
- **Find, zoom, and clipboard share xterm's key handler.** `xterm-host.tsx`
  chains zoom, find, and `clipboardKeys` in that order; the first to claim a
  key stops it reaching the shell. `clipboardKeys` claims copy shortcuts but
  leaves native paste alive. `useTerminalImage` separately registers the
  capture-phase image listener described above, so an image event is claimed
  only when the current terminal has a live image handler and plain text never
  takes that path. `Ctrl+Shift+F` opens the find bar `TerminalPane`
  (`src/components/terminal-pane.tsx`) draws over the terminal, backed by
  `@xterm/addon-search`; it reports **No matches** from the addon's own answer
  rather than tracking a count. `Ctrl+=`, `Ctrl+-` and `Ctrl+0` move
  `UiSlice.terminalFontSize`, clamped to 8-32px by `clampTerminalFontSize` -
  on the way in from a keystroke and again in the store's `merge`, because a
  same-version reload never reaches `migrate` and xterm does not validate
  `fontSize`. The size is one persisted preference behind every terminal,
  applied to the live instance and re-fitted rather than by rebuilding it,
  which would throw the scrollback away.
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
  per-file stats and no patch text, so a snapshot bumps the run's `revision`
  and the tab re-fetches the current diff from `GET /api/v1/run/<id>/patch`
  (`docs/local-gateway.md`) whenever that has moved past the `fetched`
  revision the stored patch answers for. Counters rather than a stale flag,
  because a snapshot landing *during* a request would write true over true
  and then be cleared by the response: the answer records the revision it was
  issued at, and anything newer asks again instead of showing a diff that is
  behind while calling itself fresh.
  A failure records the revision too, so it cannot spin; the next snapshot or
  the Refresh button asks again.
- **Selecting a snapshot renders that interval.** The chronological list is
  the `run.diff` events themselves - time, file count, totals - and each one
  also carries the tree it wrote and the tree before it. Selecting a snapshot
  fetches `GET /api/v1/run/<id>/patch?from=<parent_tree>&to=<tree>`, the diff
  between those two trees, which is what the run changed in that interval;
  clearing the selection goes back to the current diff against the fork point.
  An interval is addressed by two tree ids and so can never change: the tab
  keeps each one it has fetched and never asks again, and the revision
  counters above are only for the cumulative patch, which does move. The
  selection keys on the snapshot's timestamp, so a new snapshot prepending to
  the list never retargets it. A snapshot carrying no tree - one from a server
  that predates them - is not selectable. The list is capped at 40 per run and
  starts empty on every page load, because there is no history to replay.
- **The timeline yields to the patch on a narrow screen.** Beside the patch
  from `md` up it is a list. Below that it sits above the patch, where the
  header, the tabs, Land, the stats row and the local-review block are
  already between the member and the first line of code, so it becomes a
  disclosure that starts closed. With no snapshots the stacked layout drops
  it entirely and the grid is one column, because two lines saying the list
  is empty are two lines of the phone's screen; beside the patch it stays,
  and says "Nothing since you opened the dashboard." - there it is the only
  thing that explains why there are no intervals to pick. The list starts
  empty on every page load and fills as the run works, so empty is the
  normal state either way.
- **Colour is the whole of the highlighting.** `parse.ts` splits the unified
  diff into files, hunks and line kinds; `patch-view.tsx` paints those kinds.
  The dashboard never edits code, so there is no editor and no language
  grammar - the core spec's cut-line, and why neither Monaco nor CodeMirror is
  a dependency. A truncated patch parses to a last file with fewer lines, and
  the view says the diff was cut short rather than failing.
- **Long lines wrap or scroll, and the pointer picks which first.** Wrapping
  breaks the column alignment a diff is read by, and side-scrolling means
  panning every file section separately - which a phone cannot do well. So
  **Wrap lines** in the stats row is a toggle, starting on for a coarse
  pointer and off for a mouse. The choice itself is a view preference on the
  UI slice (`diffWrap`), stored like the sidebar width, because only one
  run-detail route is mounted at a time and component state would forget it
  on every trip to the Terminal tab. The Files tab's diff pane reads the same
  preference: it has no toolbar to put a toggle in, so it never sets one, but
  a member who turned wrapping off on the Diff tab meant it for diffs and not
  for one tab of them.
- **The verbs are not here, the answers are.** The tab keeps what is only
  about reading the diff - the refresh, the snapshot list, the two copyable
  `git` commands that review the run branch in the linked repository, and the
  output of the last pull (`review-commands.tsx`). Fetching the branch and
  closing the run are verbs, so they sit in the run action bar in the header
  with every other verb rather than a second time in the tab; the fetch output
  is an answer rather than a verb, so the pull records it on the `local` slice
  and the tab shows it where a member reviewing the branch will look. The
  whole block is gated on the `pull` local verb, the same one that fetches
  the run branch into the repository it explains: a gateway without it - a
  phone on the server's dashboard - has no repository for those commands to
  run in. Workspace Source control is separate from this run-branch pull.
- **Conflict chips write their list out for a finger.** The overlapping file
  names live in the chip's hover tooltip, which a touch screen has no way to
  open, so on a coarse pointer the same list is rendered as visible text
  beside the chip. A tooltip is a hint for a pointer, never the only copy of
  a fact.
- **Conflict chips are advisory.** `conflict-chips.tsx` registers into
  `card:chips` and the Diff tab renders the same component in its header. It
  reads the overlap set the conflict radar reports (`run.overlaps` at
  hydration, then `run.overlap` events), names the file and the other member,
  and navigates to their run. The event payload names peer runs but not their
  owners, so attribution comes from the runs the store already holds; an
  empty peer list means the overlap cleared and the chip goes.

## Team surfaces

`src/routes/team/` is presence, the shared approval inbox, the workspace
activity feed and budgets - the four readouts of the team features
(`internal/approvals`, `internal/timeline`, `internal/cost`). None of them owns
a view of its own in the shell: they reach the run card and the status bar
through the slots those surfaces expose, and the two full views are registry
routes (`approvals`, `timeline`), reached from the sidebar nav and the palette
like every other view, and gated on the same method the nav gates them on.

- **They refresh from the event cursor, not a timer.** Every event the store
  applies advances `lastSeq`, and that is the only signal available that a
  teammate may have changed one of these reads - the gateway has no push
  channel for a roster or a queue. `useTeamRefresh` in `src/routes/team/sync.ts`
  re-reads them when the cursor moves, with a floor between refreshes so a
  chatty run does not become a request per event. It is mounted from the
  status-bar contribution, the one surface that is always on screen, which is
  also where the presence heartbeat lives. It also refreshes and beats on
  `onWake`, because a backgrounded tab freezes both timers: a phone returns
  with its presence already expired server-side (the TTL is 45s) and with no
  cursor movement to show an approval that arrived while it was away. The
  refresh keeps the same floor as the debounced one, so app switching cannot
  turn into a request per workspace each time; the heartbeat is one request
  and always goes.
- **One refresh covers every workspace, and there is only the one.** These
  reads are per workspace on the wire, and a workspace is a repo plus its
  team settings. A deployment has a handful of them and they outlive every
  run in them. So `refreshTeam` reads all of them each time rather than
  splitting into a bounded recurring pass and a wide occasional one. Both
  readouts it feeds ask a whole-deployment question anyway - the status bar
  claims the worst budget state anywhere, and the badge claims the size of the
  whole queue - and a workspace does not stop being over its cap or holding an
  undecided request when its last run finishes, so no subset could answer
  either one. Failures leave the last good data in place.
- **An unreadable queue says so.** `refreshInbox` keeps the first
  per-workspace `approval.list` failure, and the inbox, the nav entry and the
  status-bar chip report it rather than "Nothing is waiting on a decision.",
  which over a failed read means "no agent is blocked". Workspaces that
  answered are still listed. Each read is stamped, so a slow failure cannot
  overwrite a newer good answer.
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
- **A feed row names the event's type; it does not print it.** The name comes
  from `src/lib/events.ts`, which is also where the type filter's options are
  named from, so an option and the rows it selects cannot call one type two
  different things. The wire string stays as the row's tooltip. The describer
  table in `src/components/feed-entry.tsx` is keyed by the map's own type, and
  `filterTypes` by those keys too, so a type cannot gain a name without a
  description, or the reverse, without failing the build. A type the map has
  never heard of renders as its wire string, because a server newer than the
  dashboard can emit one.
- **Activity run links preview labels to two lines**, while the button retains the full label in its accessible name and `title`.
- **Below `sm` the filter bar folds.** Four labelled selects are most of a
  phone screen before the first entry, so only the workspace one - the filter
  that scopes the feed at all - stays out, and Run, Member and Type go behind
  a **Filters** disclosure. Its caption counts the three that are set, so a
  feed narrowed by a filter the member cannot see is not read as an empty
  log.
- **A budget warns, it never stops anything.** The status bar shows the spend
  and the worst state any workspace is in (`ok`, `warn`, `exceeded`) - every
  workspace, ones with nothing running included, which is what the wide read
  above is for - and says so in those words, naming each workspace and its cap
  in the tooltip. A spend that includes unmetered runs renders as a floor
  (`$1.20+`), because a harness with no adapter reports nothing.
- **Workspace controls live on the workspace view, not the status bar.**
  `routes/workspace.tsx` is one workspace: its name, its base branch, its runs,
  and buttons for the budget (`budget.set`), the steering policy
  (`workspace.settings`) and, for admins, Source control. Each is gated by its
  server-side permission. A spend ceiling and a steering policy belong beside
  the thing they govern rather than in a header two views away. The settings
  dialog shows the base branch without offering to change it: runs have already
  forked from it, so editing it there would only make the displayed branch
  disagree with the branches on disk.
  Source control reports local-only, pending, ready or failure, including the
  configured source, branch, accepted SHA and last check time. One-time Configure
  starts from `Workspace.Origin` and offers public or deploy-key access; the
  latter exposes only a public key to copy and the GitHub deploy-key link.
  Verify/Refresh reports the server's result; **Adopt candidate** and
  **Disable mirror** require explicit confirmations.
- Watcher avatars come from the roster's `watching` set, which the gateway
  fills from live PTY attaches - the browser's attaches included.
- The same refresh reads `GET /api/v1/disk` and writes it onto the stored
  `server.info`, which is what fills the status bar's disk gauge.

## Manage workspaces

`src/routes/workspaces/` renders a flat, bordered list of workspaces. Each row
shows its name, creation time, base branch, steering policy and an **Open**
button.

## Settings

`src/routes/settings/` is a local-gateway surface. It shows the local link,
the sync daemon and **Mirror run files to your repository** in flat bordered
sections. That section starts and stops a live run's sync overlay. The
server-hosted dashboard omits these machine-local settings and controls.
Workspace base freshness and Source control live on the workspace page. A
configured mirror is refreshed there with **Verify/Refresh**; a local-only
workspace uses its local base. There is no recurring base-refresh button.

## Onboarding wizard

`src/routes/onboarding/` is the local-gateway guided first-run path, six
steps: Link, Git identity, Workspace, Repository, Agents, First run. It
renders only where the gateway serves the client-machine verbs (the capability
descriptor lists `link.status`); a gateway without them gets an explanatory
empty state instead of a broken wizard. That copy names the gateway and what it
cannot reach - an SSH identity, a repository on the member's own computer -
rather than guessing at the device in the member's hand, which the app has no
way to know. The server-hosted dashboard has no machine-local onboarding
wizard: it starts at the board, while shared runs, terminal, Files and
configuration surfaces remain available through the server gateway. Link,
Workspace and First run live in `steps.tsx`; Repository is `repo-step.tsx`, Git
identity is `git-identity-step.tsx`, and Agents is `agents-step.tsx` with its
GitHub part in `github-connect.tsx` and its configuration import in
`profile-import.tsx`.

Navigation is two levels: the step index, and one sub-screen name owned by
whichever step has sub-screens. The Agents step owns both of today's - a
harness's setup screen, named by the harness, and the GitHub connect screen,
named `@github` - and the wizard holds the name, so **Back** closes an open
sub-screen first and leaves the step only from the step's own screen. A step
with sub-screens takes them as `setup` and `onSetup` rather than keeping them
in its own state.

The wizard owns that Back button but passes it down as a `back` node, and each
step puts it in its own action row, beside Create workspace, Skip for now or
Launch. Those rows stick to the bottom of the wizard's scroller, so a step as
tall as Agents with the terminal dock open cannot push the way on and the way
back off screen. The exception is the Repository step before a clone is
connected, where Back sits in the path form beside **Add remote**; that screen
is one field long.

In the step header, every step the member has already reached carries a check
and is a button that jumps to it, forwards as well as back, so walking
backwards does not strand them on a step they cannot leave. The current step
and the ones never reached are inert text.

The Git identity step collects the name and email the member's commits are
authored as, saved on the server with `member.git`. Where the gateway serves
`git.identity` it prefills them from this machine's own `git config`, without
overwriting a field the user has typed in; what the member already saved wins
over both. **Skip** moves on and leaves the server's fallback in place, so the
step never blocks the wizard. See [teams.md](teams.md) for what the identity
does once it is set.

The Repository step asks for a clone on this machine, and the path must be
absolute - a leading `/`, a drive letter, or a UNC prefix, all three accepted
whatever the machine is, because the check only catches a plainly relative
path and a Windows dialog answers `C:\...`. The field always offers the
folders `link.status` already knows - the linked clone and every named
profile's - as a `datalist`. In the desktop shell it also gets a **Choose
folder** button, which opens the native directory dialog through
`window.aetherDesktop.chooseFolder`, writes the answer into the field and
clears any error the last attempt left; cancelling leaves both alone, a
dialog that fails puts the shell's own error under the form, and the whole
path form waits while a dialog is open, because a chooser that is not modal
to the window leaves it live and a late answer would land on top of whatever
was typed or submitted meanwhile. That wait is the step's own state, so
leaving the step and coming back is the way out of a chooser that died
without answering. Typing stays the fallback, because a browser tab has no
dialog and a shell built by an older `aether gui build` has no method.

The step adds the `aether` remote (`link.repo`) and then seeds the workspace:
where the local gateway serves `repo.push` it shows a **Push now** button.
The gateway compares the clone's base branch with the workspace's copy before
pushing and answers with one of four states, so the second member to join a
workspace reads what happened instead of git's `! [rejected] main -> main
(fetch first)`.

Local-only workspaces retain **Push now**, **Fast-forward my clone**, daemon
base pushes and direct base writes. A mirrored workspace owns its base on the
server and rejects direct base writes; run branches can still be pulled. After
repository linking, onboarding may link to the
workspace's Source control, but it never silently authorizes a source.

When `link.repo` answers an `origin`, the connected line adds `Runs push to
<origin>`: the upstream a run pushes to, the same one `aether link --repo`
prints. That push destination is separate from the Source control read source.
A link that recorded none says nothing about one.

`pushed` names the branch that landed; `up-to-date` names the commit the
workspace already has. Both keep git's output in a "What git did" panel,
open on arrival because `Everything up-to-date` and `[new branch]` are both
success and mean different things, and Continue moves on.

`behind` means the workspace is ahead. The step names both tips and offers
**Fast-forward my clone**, which runs `repo.fast-forward` and then reports
the new tip, whether that branch was the checked-out one, and whether the
working tree is dirty. The panel then shows both outputs in the order git
produced them: the comparison's fetch, then the fast-forward. `diverged`
means both sides moved on: the step names both tips and offers the fetch,
log, rebase and push commands to resolve it by hand, copyable as one block,
with no fast-forward button, because Aether never force-pushes.

Both states keep **Push now**, which re-compares - the thing to do after
resolving by hand - and both take away the copyable `git push -u aether
<branch>`: it is the command that produced the rejection this comparison
exists to replace. That command stays only where it is the right one, which
is a clone the workspace has not moved past: before any push, after one that
failed, and after one that landed. A refusal keeps the user on the step with
git's own output in a monospace block. The three outcome panels - behind,
diverged and the fast-forward result - are `aria-live="polite"`, because they
appear without a page change.

The branch is the workspace's base branch, so a workspace created with
`--base` seeds the branch its runs fork from. When `repo.push` is unavailable,
the step shows only the copyable command.

What the step settled - the clone path, the remote the gateway wrote,
git's push answer, and the fast-forward once one has run - lives on the UI
slice as `onboardingRepo`, not in the component. Each answer is written onto
the record as it stands in the store rather than the one captured at render,
so a push and a fast-forward started from the same screen cannot lose each
other's result. Each answer also carries the clone it ran against. A member
who re-points while a request is in flight has that answer - and its error -
dropped rather than merged onto the repository that replaced it, and the new
form starts with its own push button rather than the old request's spinner.
A member who picks another workspace keeps the answer on the record it was
issued for, and the step, which shows only the record matching the selected
workspace, never renders it against the workspace that replaced it.
Returning to the step shows that connected
repo with **Use a different repository** to go back to the form, prefilled
with the old path; a blank form there would ask again for a remote that
already exists.

The UI slice persists the resume point, the furthest step reached, the selected
workspace, the connected repository and the First run draft; finishing the
wizard or navigating away from it clears all five. The step is stored by name
rather than by position, so inserting a step - as "Git identity" was - never
relocates someone who is mid-wizard. The persisted state is versioned, and one
migration in `web/src/store/index.ts` covers every older shape: versions 0
through 2 stored the resume point as an index, so those numbers are read back
as the steps they named; versions 0 and 1 stored a Repository answer this build
cannot use - version 0's predates the comparison states and version 1's carries
no link id - so it is dropped rather than rehydrating a blank panel; and
no version before 4 has a furthest step, so the resume point becomes it, or
the first backward jump would turn every later step inert. Anything it cannot
place starts over. Repository is where the workspace first becomes
load-bearing, so resuming onto it or any later step without one falls back to
the workspace picker; the steps before it resume where they were.

Hydration reads `link.status` first for a local gateway: a linked machine is
marked onboarded before the redirect decision, so it never re-enters
onboarding after a fresh GUI launch. An unlinked local gateway still routes
here when `onboarded` is false. The server-hosted gateway has no local link
state and opens at the board. Completing the final step or navigating
elsewhere marks the UI onboarded and clears that wizard state.

The Link step distinguishes no configured server, a server with no repository,
and a fully linked server. It refreshes on Retry and when the window regains
focus, so a separate `aether link` command appears without restarting the GUI.

The Agents step has three optional parts and never blocks: **Skip for now**
is reachable from every state, including an open setup shell and a failed
configuration import.

Part A lists the setup-capable harnesses from `env.harnesses` against
`agent.list`, saying for each whether it is installed on this machine and
whether the server lists that name. The copy states what those two signals
actually mean - `agent.list` includes shipped harnesses even when the server
account has not installed them, so the list is not a "set up" badge - and **Set
up** embeds the same `AgentWizard` the Agents page uses, driven with the
harness and workspace already known so it opens the `agent-setup` shell
without a form. Setup confirmation checks `agent.list` for an installed
executable, then runs `env.save` - the call the dock's **Save environment**
button makes - before handing that agent to the First run step, which
preselects it. The save is the point: an executable that exists only in the
running container is not in the image runs start from. The done screen names
the saved image. A missing executable, a failed check or a failed save keeps
setup open for retry with the real error. The vendor login is not checkable
from here, and the copy says so.

Between the two, `github-connect.tsx` connects the member's GitHub account.
The closed `<section aria-label="Connect GitHub">` says what a connection
buys - runs push branches and open pull requests as the member, and commits
are signed with a key kept in their environment home - and **Connect GitHub**
opens the sub-screen. The sub-screen mounts the same `TerminalDock` the
setup screen uses and calls `github.probe` once that dock reports a running
container: the probe runs `gh --version` inside it and refuses when there
is nothing to run it in. Until then the screen says it is waiting for that
terminal to start, and once it has one, that it is checking it for gh;
through both it shows no login command and types nothing. **I've logged
in** stays live there on purpose: `github.connect` makes the same check
itself, so it is the way out of a probe that never settles.

Once the probe answers with a gh that can do the login, `initialLine`
becomes `typedLoginCommand` from `src/lib/github.ts`: `gh auth login
--hostname github.com --git-protocol https --web --scopes
admin:ssh_signing_key`, prefixed with Ctrl-U. The member can have typed at
the prompt while the check was out, so the line clears it rather than
landing on top of it. Ctrl-U kills backward from the cursor, so anything to
its right survives and is appended to the login command; Ctrl-K would cover
that, but the terminal falls back to `/bin/sh` on an image without bash and
dash passes Ctrl-K through as input, which breaks the command. The byte is
raw input either way, so a member sitting in an editor or a pager gets it as
one. The screen echoes the command itself, without the prefix, in a code
block for anyone who would rather type it, and says what that login looks
like from inside a container: gh asks the member to press Enter to open a
browser and then reports that it could not open one, so the member presses
Enter, ignores the failure and opens the printed URL with the one-time code.



A probe reporting no gh, a gh that would not run, or one too old for the
login check replaces the login command and its explanation - not the dock,
which is where the remedy is carried out. In their place comes the screen's
own sentence about the gh the probe found, then the server's commands as
`CopyableCommand`s - the admin one when there is one, then the member's -
and gh's own answer in the same monospace pane refusals use; no login
command is offered at all, and **I've logged in** is disabled, because
connecting would only collect the matching refusal.

There are three shapes. A gh the member installed into their own
environment home - which the probe reports as `path`, and which comes first
on PATH and survives every image - is named as the file it is, with no
image command at all. A member on their own saved image is
offered the install-and-save that keeps it first, and told that `aether env
reset` removes it. A member on the server's standard image is told an admin
runs the command, or just to reopen the terminal when the image has already
moved without them. Every remedy the copy names is a button on the dock
right below: **Save environment**, **Reset to standard**, **Stop
environment**. **Check again** re-runs the probe, because none of those
three restarts the container by itself.

A probe that fails, or a dock that never got a terminal at all, puts the
login command back - shown, not typed, and said to be untyped - because a
member whose gh is fine must not be stopped by a check that could not run.
A dock that has a terminal and merely refused a write is not that, and does
not release it. A failed probe renders its own error above **Check again**;
a dock that could not open reports in its own pane, and gets no button,
because a second probe cannot run without a container and the attach is
already retrying.

**I've logged in** calls `github.connect`, which does the non-interactive
rest on the server; success names the account and the signing key's
fingerprint, and **Close** returns to the step, which then
reads "Connected in this session as `<login>`" - the connection is React
state that a reload loses, said the way the agent rows say "Set up in this
session". A connection counts the way a set-up agent does for the step's
primary **Continue**. Server refusals - most often "not logged in to
github.com in the environment terminal" - render verbatim in the same
monospace pane the Repository step gives git's output, because gh's answer
runs to several lines, and leave the screen open to retry.

Where the gateway serves no terminal socket the screen gives the CLI path
and nothing else: `aether terminal`, the same `gh auth login`, then `aether
github connect`. There is no **I've logged in** button there - with no
terminal to log in through, the whole flow is the CLI's. The login command
itself lives in `src/lib/github.ts`, so the screen and the Playwright spec
assert one string.

**Configuration import** is an explicit, one-time directory import in the
local onboarding flow. `config.roots` supplies destinations such as
`~/.claude`, together with the destination-specific runtime paths that can be
left out locally. The browser waits for that response: a known unique basename
selects its destination automatically, while an unknown or ambiguous basename
requires a destination choice before previewing or reading any file bytes.
The raw browser `File` handles stay local so changing the destination clears
the old preview and re-reads with the new policy; a generation guard prevents
a slower old read from replacing the current preview. This action is not
available from the server-hosted dashboard, because that browser cannot read a
machine-local directory; the server dashboard still exposes Files and
configuration editing through `config.tree`, `config.read` and `config.write`.
There is no local discovery scan or directory watcher; the picker is the only
onboarding import action.

Credential names found in any path component and `*.pem` files are always
left out in the browser. Runtime/history paths come from the selected
destination's `runtime_ignores` metadata and use exact, root-relative
case-sensitive component-prefix matching. Every other selected byte is
uploaded and server-scanned; the result reports accepted file/byte counts and
server exclusions. A response with `error` is an incomplete import: the UI
reports the committed counts, exact canonical `imported_paths`, and the real
error instead of showing success, and warns that copied files remain. If the
RPC fails without a response, the outcome is unknown (some files may have been
copied); inspect **Files** before retrying. There is no watcher or automatic
retry - choosing a directory and importing again is always explicit. The
import limits are 2,000 files, 1 MiB per file and 20 MiB decoded in aggregate,
with a 30 MiB HTTP request cap. Empty and binary regular files are preserved,
but browser imports send mode `0644` and cannot preserve executable mode or
symlinks.

Accepted files change the calling member's persistent home immediately,
including for already-running agents that share that home; an agent may need
to reload. Auth/vendor login is separate.

The First run step is the last one, and launches a run in the workspace the
Workspace step settled on. Its **Agent** select offers only the entries
`agent.list` reports as `installed` in this account - the launch form's rule
without its `custom` escape hatch - and the agent the Agents step just set up
is preselected when it is one of them. With none installed the step drops the
picker: it says a run launches an agent in a container and none is installed
yet, and offers **Set up an agent**, which jumps back to the Agents step.
Skipping setup and then picking a shipped name is how a member used to reach
the server's refusal only after the run had launched. A failed `agent.list`
shows the server's error with **Retry** as the primary action and no setup
button, because a gateway that could not answer is not the same fact as an
account with nothing installed.

The step's "No agent subscription yet?" note points at the CLI and stays on
screen once a workspace is chosen; "Prove the plumbing without an agent subscription" in
[quickstart.md](quickstart.md) covers what `fake` is and how to launch it.

## Update prompts

`src/components/update-banner.tsx` is where the dashboard says a binary is out
of date: it hosts the banners, with the CLI one in
`src/components/cli-update-banner.tsx` and the pieces they share in
`src/components/update-banner-shared.tsx`. It is mounted by `AppShell` above
everything else, because an out-of-date binary is about the whole app rather
than the view that happens to be open. The CLI and shell prompts read
`update.check` from the local gateway, because the dashboard can update only
the machine running `aether gui`. The server prompt asks the linked server
about itself and appears wherever the member is an admin.

- **Two reads.** The host reads `update.check` on mount, again every half
  hour, and again whenever the window comes back to the front, on `focus` and
  on `visibilitychange` alike. No read runs while the window is hidden, the
  timer included, so the freshness this buys is best-effort on a minimized
  app and immediate when the member returns to it. The desktop app runs for
  days, so a release that ships after launch has to arrive on its own, and
  the half-hour timer is half the gateway's cache period
  (`docs/local-gateway.md`) rather than all of it, which bounds the wait at
  the two added together. One reader serves both the re-checks and the
  Update button. A re-check never starts while a lookup is out, and every
  read is numbered so that only the newest one's answer is written: a read
  issued before the click is served the cache the click has yet to refresh,
  and would otherwise bring the superseded release back. The re-checks also
  stand down for the length of an install, so nothing renames the banner
  over the release being written to disk. A successful lookup is cached for
  an hour, so the re-checks cost the gateway one request to GitHub an hour
  at most; a failed one is cached for five minutes, so an unreachable GitHub
  is retried sooner. The answer goes on the `local` slice, which is also
  what the status bar reads. A re-check that fails is swallowed, as the
  first read always was: it leaves the last good answer standing rather than
  blanking a banner that is already on screen, and the next re-check still
  runs. It reads `server.update_status` as well - any member may - and
  re-reads it on every reconnect and whenever `server.info` names a different
  version. The reconnect is the one that matters: a server that updates
  itself re-executes, so the socket drops and
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
  release link where the platform has no self-update at all. Clicking
  **Update now** first re-reads `update.check` with `refresh: true`, which
  skips the gateway's cached answer, and puts that on the store, so the
  version the banner names while the download runs is the release that was
  newest a moment before `update.apply` resolved its own. A fresh answer
  saying this machine is already current - the update ran in a terminal
  while the app was open - installs nothing and says so, *Aether vX is the
  newest release. Nothing was downloaded.*, rather than taking the banner
  away under the click. That notice is about the answer it was made from:
  when a later release lands, the offer and its button come back. A fresh
  check that cannot reach GitHub shows the gateway's message and leaves the
  button usable. Otherwise the call goes on to `update.apply` and the banner
  follows the phases below; nothing else reconnects, because the existing
  `ConnectionError` page already owns a gateway that goes away. The done
  state names what the gateway said is left to do, every binary the swap
  replaced and, on a single-box install where `aether-server` was one of
  them, the `restart_command` the gateway sends back: the server keeps
  running the old code until its unit restarts, and the CLI prints that same
  line. Everything from `update.apply` onwards is rendered from that answer
  rather than from `update.check`, because the re-checks carry on behind it
  and report this machine as current the moment the swap lands - which would
  otherwise take the restart command, and a rebuild still running, off the
  screen. The offer headline gives way to *Aether vX is installed.* there,
  since the release is no longer on offer. A `-32001` (denied) answer is the
  dialog cancelled or the password refused: the banner shows *Update
  cancelled, nothing was changed.* muted rather than as a failure, and the
  button comes back. Any other refusal is
  rendered verbatim -
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
  reload, and the next release shows the banner again - without a reload,
  when a re-check is what brings that release in. It silences the offer,
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

- **Tokens only.** See [styles.md](styles.md) for the VS Code-inspired
  Light Modern and Dark Modern semantics, interaction tokens,
  `--state-*` tokens, geometry, radii, fonts and the one inline-colour
  exception for member attributes. Components use semantic token classes rather
  than route-specific colour literals.
- **Dark, light, system.** The preference is stored, and `system` follows
  `prefers-color-scheme` live. There is no additional theme mode.
- **Typography and density.** The system UI stack is 13px with 12px
  supporting copy and 1.4 line height. JetBrainsMono NFM remains terminal and
  code; VT323 remains only the Aether wordmark and original startup. Use the
  35/48/35/22/26/22px workbench geometry and avoid promotional titles or
  oversized cards.
- **Flat shell and palette host.** Use a 35px title/command bar, persistent
  48px activity rail and adjacent 200-520px workspace/run sidebar. Mount the
  command palette once in `AppShell`; keep one status Slot for other live
  contributors.
- **Two glyphs, two layers.** The harness glyph says who is running, the state
  dot says what state - never merged into one mark. Presentation states
  (`working`, `waiting`, `needs-attention`, `failed`, `done`, `idle`) are
  derived in `src/lib/status.ts`; the domain status enum is untouched. A group
  header shows the worst state of the runs under it.
- **A working run moves.** `StateIndicator` swaps the static dot for three
  dots bouncing in `--state-working` on board cards, run headers and run lists.
  Sidebar rows keep one dot and pulse its opacity; palette rows stay static.
  The fixed dot box prevents a row shifting when a run starts or stops.
- **Motion is optional.** The steering signal, working dots and sidebar pulse
  answer `prefers-reduced-motion: reduce` by removing movement. The original
  shooting-star scene appears only at desktop startup and is skipped under
  reduced motion. There is no reveal-flash animation. Spinner and skeleton
  feedback remains available.
- **Primitives first.** `src/components/ui/` holds the shadcn/ui pieces:
  `Button`, `Input`, `Textarea`, `Label`, `Select`, `Checkbox`, `Collapsible`,
  `Dialog`, `AlertDialog`, `DropdownMenu`, `Command` and `Skeleton`. These are
  house copies, retuned to the shared scale and `focusRing`, and preserve props,
  events, refs and accessibility contracts; do not overwrite them with registry
  defaults. Use them instead of raw form elements. Shared fields and buttons are
  26px; compact tools are 22px; adjoining panes and rows are square, and
  controls use bounded 2-4px radii. `Input`, `Textarea` and `SelectTrigger`
  compose the shared `field` style with contrast-tuned `--input` borders,
  readable placeholders and explicit disabled/read-only states.
- **The empty string belongs to the Select placeholder.** Use a named,
  non-empty sentinel for an empty API or filter value, then map it back at
  that boundary. The Select wrapper ignores the empty report Radix can send
  through its hidden native select while options arrive.
- **Name the Select trigger.** Pair the caption's `htmlFor` with the trigger's
  `id`, so the control announces its purpose rather than only its current
  option.
- **Disclosures normally unmount closed content.** `CollapsibleTrigger`
  supplies the marker. The status bar uses `forceMount` with closed-state
  hiding to retain its live contributors while the popup is closed.
- **HeroUI has a narrow role.** `src/components/ui/heroui.tsx` wraps HeroUI v3
  `Chip` and `Tooltip` and maps them to the house tokens. Use `Chip` for status
  or metadata, and `Tooltip` for supplemental hover and keyboard help. Run and
  dock tab strips retain their custom manual tab semantics. A `Chip` takes no
  `title` of its own, so a pill whose text can be clipped wears one on the
  span around it.
- **A hint a reader needs is a `Tooltip`, not a `title`.** A `title` is drawn
  by the pointer and by nothing else, so on a control a keyboard can land on
  it is information that reader can never get at. A Tooltip opens on focus and
  hover, with the shared wrapper's 300ms hover delay, and points the control's
  `aria-describedby` at itself. It is a description rather than the name, so
  an icon-only control keeps its `aria-label`. A `title` stays only on what a
  keyboard cannot land on - a truncated path, a timestamp, a breakdown -
  since React Aria makes a tooltip trigger focusable, and a hint on a mark
  would buy a tab stop per feed row or per card. A mark that names itself
  needs `role="img"` first: a bare `<span>` is `generic`, and ARIA gives
  `generic` no name.
- **A yes-or-no confirm is an `AlertDialog`, not a `Dialog`.** The role
  interrupts rather than announcing a form, it takes the close X away, and an
  outside click no longer dismisses it. A dialog that asks which of several
  outcomes to record - closing a run as merged or abandoned - is a choice
  rather than a confirm, and stays a `Dialog`. `AlertDialogAction` is
  destructive unless the caller says otherwise. A confirm that reports its own
  failure prevents the default on that click and closes on the answer instead,
  or the refusal would be unmounted with the dialog.
- **A control a reader still needs is `aria-disabled`, not `disabled`.** A
  `disabled` button takes neither focus nor a pointer, so anything it had to
  say - the full verb behind a shortened label, why a row cannot be opened -
  goes with it. Those keep their place in the tab order, guard their own
  handler, and drop the hover the enabled state paints.
- **One focus indicator, one source.** `focusRing` in `src/lib/utils.ts` is the
  shared outline utility. Preserve its 2px outside outline, inset behavior for
  full-bleed rows and forced-colour visibility. Menus retain roving focus,
  typeahead, portalling and viewport flipping. Browser checks verify the
  resulting focus outline; source scans and class-name assertions are not
  behavior coverage.
- **Member colour attributes, it does not fill.** The avatar rings itself in
  the member's colour and keeps its initials in the foreground token, because
  the colour is arbitrary server data with no contrast guarantee in either
  theme.
- **Loading feedback matches duration.** Skeletons appear only after ~200ms
  (`useDelayed`), so a fast response never flashes.

## Tests

The dashboard tests use Vitest, jsdom and Testing Library for observable
client behavior, with fixtures and stub API/WebSocket transports where a
server is not required. Store slices, selectors, token bootstrap, stream
lifecycle, terminal attach/reconnect behavior, permissions and error paths
remain covered as state transitions. Component tests should assert labels,
accessible names, focus handoff, keyboard actions, navigation, loading and
empty states, server errors, capability gates and mutation results.

`src/a11y.test.tsx` exercises the run tab strip, dock tabs and sidebar splitter
with keyboard events: arrow navigation, Enter and Space activation, Delete and
Backspace close, focus handoff, clamped resizing and collapse. `src/components/shell/nav-shortcuts.test.tsx`
covers the shell shortcut precedence across fields, dialogs, menus, lists and
selects. `src/components/ui/fields.test.tsx` checks that a wrapped Label names
its actual input and that an Input ref reaches the field DOM node. These are
behavior assertions against rendered controls; source scans, literal class
assertions and CSS text checks are not behavior coverage.

Route tests cover the board, run detail, terminal, diff, files, workspace,
onboarding, team, members, settings, agents, templates, palette and update
surfaces. Keep assertions on what a member can observe: a route or control
appears or disappears under its capability and role, a focus or navigation
action lands in the expected view, a loading or error message is shown, or the
API receives the mutation only after the relevant confirmation. Preserve
verbatim gateway errors in assertions when they are part of the contract.

`web/e2e/` is the layout and browser-behavior layer. Playwright drives a real
browser against the local `aether gui` gateway and the server it proxies, while
the same static bundle is also usable through the server-hosted gateway.
`keyboard-focus` checks Escape ordering across an open dialog and run view and
confirms that a focused control paints the app outline. `window-sizing` and
`status-bar-sizing.spec.ts` exercise update notices and status controls at
narrow and desktop dimensions.

The touch shell is driven by the `mobile` project, which
[testing.md](testing.md) describes: `shell-drawer.mobile.spec.ts` opens the
phone drawer, taps a run and finds the drawer gone with the run on screen;
`dialog-anchor.mobile.spec.ts` checks that a dialog short enough to tell the
two apart sits at the top rather than the middle, and that the launch form
keeps its footer on screen on a viewport as short as a keyboard leaves;
and `toast-clearance.mobile.spec.ts` checks that a toast comes to rest above
the status bar rather than on top of it. `run-views.mobile.spec.ts` steers a
real run from the header's Actions menu and then reads its diff. `sidebar-drawer.spec.ts` stays on the desktop project because its keyboard contract -
`Mod+B` closing the drawer and the palette coming back once it is gone - needs
a narrow window with a keyboard rather than a phone. `board-card`, `run-switch`,
`run-attach-retry`,
`run-provisioning`, `terminal-tools` and the onboarding scenarios cover the
corresponding real UI transitions, gateway responses and terminal behavior.
Run the full browser workflow with `make test-e2e`; its scenario inventory and
setup details live in [testing.md](testing.md).

Use the browser workflow for real computed geometry, titlebar and activity-rail
behavior, responsive overflow, keyboard focus and gateway-backed transitions.
Exercise both themes and narrow and desktop widths against the actual surface;
do not pin CSS classes, source strings, token declarations or component
plumbing. When changing a visible contract, update the assertion at the layer
that can observe it. Use a component test for state, text, role, focus and
navigation behavior, and Playwright for computed layout, actual browser focus
outlines, responsive overflow, Escape ordering and gateway-backed flows.

### Testing on a real phone
The browser suite exercises the local gateway path; the server-hosted gateway
and a real touch device are checked by hand. Give the server a dashboard port
and restart it:

```sh
sudo aether-server config set web-port 443
sudo systemctl restart aether-server
```

Then open `https://<the server's MagicDNS name>/` on a phone joined to the
same tailnet. Expect the board as the first screen, already identified by
WhoIs: no onboarding wizard, no Settings, no link chip, no update banner, and
no pull, forward or sync controls, because the descriptor carries no `local`
verbs. Files and configuration editing remain available through the
server-hosted gateway. Watch what the server saw with:

```sh
journalctl -u aether-server -f
```

Prerequisites and the refusals a bad tailnet setup produces are in
[networking.md](networking.md#the-dashboard).
