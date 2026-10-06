# Dashboard SPA (`web/`)

The browser client is one static bundle served through either dashboard
gateway: `aether gui` on the user's machine, or
`aether-server --web-port` over HTTPS on the server's tailnet addresses.
Next.js 16.3.4 produces the static export, while React 19 + TypeScript render
the client runtime, Tailwind v4 and the Radix-based primitives in
`src/components/ui/` provide the components, and Zustand holds the state. Both gateways use the shared `internal/webgate`
API and WebSocket surfaces; the server gateway authenticates each request with
Tailscale WhoIs and no browser token.

The visual contract is a dense developer workbench on graphite surfaces with
one teal accent: flat panes and compact controls. `docs/styles.md` defines the
tokens, type and motion.

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

`?run=<run_id>` is read the same way, by `takeRequestedRun` beside the token
reader in `src/lib/api.ts`, and `hydrate` in `src/store/sync.ts` acts on it
once the runs are in the store: it navigates to that run's terminal, or
leaves the board alone when the run is not one this member was sent. Reading
it removes it from the address bar, which is what makes it one-shot - a
reconnect re-hydrates and must not drag the member back. It is the whole
deep-link contract for both shells; see
[local-gateway.md](local-gateway.md#running-it).

Node 22+ is required for a hand-run dashboard build. The complete contributor
toolchain and the optional desktop installer workflow are in
[CONTRIBUTING.md](../CONTRIBUTING.md#toolchain).

## Testing on a phone

The shipped phone path is the server-hosted gateway: set `web-port`, then open
the server's MagicDNS name on a phone joined to the tailnet
([Testing on a real phone](#testing-on-a-real-phone)). The connection is
HTTPS and carries no browser token; Tailscale WhoIs identifies the phone's
source address on every request. New members enter hosted onboarding even
when the server already has workspaces: Git identity, workspace selection or
repository import, agents and the first run. Machine-local linking, folder
picking and updates still require the desktop app or `aether gui`.

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

### The web app manifest

`web/src/app/manifest.ts` is a Next metadata file, so the export writes
`/manifest.webmanifest` and links it from the page. A static export refuses to
collect a metadata route that has not declared itself static, which is what the
`export const dynamic = 'force-static'` in it is for.

It declares `display: standalone` and `start_url: '/'`, and names four icons
from `web/public/icons/`: 192px and 512px in both `any` and `maskable`. 192 and
512 are the pair Chrome and MDN document; what the installability check
enforces is lower - one `any` icon of at least 144px - so shipping both
documented sizes is belt and braces. No service worker ships. Chromium's
installability check no longer looks for one; the post announcing the removal
([update-install-criteria](https://developer.chrome.com/blog/update-install-criteria))
scopes it to installing "from the menu, since version 108 on mobile and 112 on
Desktop". The dashboard could not cache anyway, because it lives inside the
server binary and has to change with it.

**Both mobile browsers read the manifest.** Safari has since iOS 11.3
(`display`, `name`, `short_name`, `start_url`, `scope`), with `theme_color`
since 15 and `icons` since 15.4, so an iPhone's standalone window comes from
the manifest, not from the layout's Apple meta tags. Those tags cover what a
manifest cannot say: the status bar style, which needs
`apple-mobile-web-app-capable` spelled out by hand because Next 16 emits only
the unprefixed `mobile-web-app-capable`. `web/public/icons/apple-touch-icon.png`
stays because Safari prefers it over the manifest icons.

A manifest carries one `theme_color` where the layout's viewport export carries
one per colour scheme, so both read `web/src/app/theme-color.ts` and the
manifest takes the dark value. `background_color` is the separate
`iconBackground` from that module, `#0a0a0a`: an installed app's splash centres
an icon on it, and any other value would leave the icon's tile showing as a
square. That hex is deliberately darker than the dashboard's own dark
`--canvas`, `#141516` - a launcher icon has to read as an object against a
wallpaper - so the splash lightens slightly as the SPA paints over it.

Every icon is generated from `web/public/aether-mark.png` by `python3
scripts/make-icons.py`, the same script that writes the desktop app's, and the
output is committed. Maskable icons and the iOS icon are square and full-bleed
because the platform applies its own mask; the rest carry the rounded tile.

CI installs Bun with `oven-sh/setup-bun` (version pinned in `web/.bun-version`)
in jobs that run `make build` or `make release-binaries`, plus a dashboard job that
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
every surface uses the same action. The route is mirrored in the address bar
(see [URL state](#url-state)).

The global `overview` route (`src/routes/overview.tsx`) is **All workspaces**:
every run in every workspace, reached from the workspace switcher, `g` then
`l` and the palette. Run-detail routes are Terminal, Browser, Diff and Events;
there is no separate per-run Overview route.


**Store slices** (`src/store/`). One Zustand store composed of slice creators,
one file each (`server`, `workspaces`, `runs`, `members`, `terminal`, `board`,
`palette`, `approvals`, `presence`, `cost`, `timeline`, `diff`, `shell`,
`local`, `messages`, `ui`). A new feature adds a slice file and one spread in
`createRootStore`. Slices are typed against the whole root state, so a slice
may read another's data. Only view preferences (theme, sidebar width and
collapse state, `activeWorkspace`, the **Mine** toggle, board layout and per-workspace
map camera, dismissed update versions, terminal zoom) are persisted;
`persistedUi` in `store/index.ts` is the list that decides. Server data is
always re-fetched.

**Selectors return primitives or stable references.** A selector that builds
a new array or object on every call re-renders its component on every store
write, and under React 19 can loop. Lists of IDs go through `useShallow`
(`useRunIDs(workspace)` in `src/store/hooks.ts`); one entity is read by ID
(`useRun(id)`); a row asks for a boolean (`selected`) or its own state
(`useRunPresentation(run)`) rather than the whole `route` or run map. Derived maps live in the reducer that
changes their inputs: `setInbox` rebuilds `approvalsByRun` (each run's
pending requests, oldest first), so a row's approval count is one lookup.
Run rows and board cards are `React.memo` components; reducers replace only
the record of the run that changed, so an event about one run re-renders
that run's row and card and leaves the rest alone.

**Relative times tick on one shared clock.** `<RelativeTime at>`
(`src/components/ui/relative-time.tsx`) renders a `<time>` whose text comes
from `timeAgo` and subscribes to `useClock()` in `src/lib/clock.ts`: one
30-second interval for the whole app, running only while something
subscribes. Only the `<time>` re-renders on a tick, so "2 minutes ago" on a
board card, a run list row, a feed entry, an approval, the members and
devices views or the diff timeline keeps moving without re-rendering the
row. A per-second countdown, such as a queued steer's delivery in the Run
Room, is its own small component and re-renders only its text.

The `messages` slice holds agent mail (`coord.messages.list`) per workspace,
swarm, or run scope, merged by message ID. A `coord.message` event carries no
body, so every loaded scope the message belongs to re-reads its newest page;
`coord.message.acked` stamps the row. Both re-read the recipient run for its
`unacked_messages`.

**`activeWorkspace` is the scope workspace surfaces read.** It lives on the `ui`
slice and names the workspace the sidebar's run list, the board, launches,
templates, budget dialogs and the activity feed all act on. Empty means "all",
which is what the board falls back to before hydration has named one.
`setActiveWorkspace` carries an open `workspace` route along with it, so the
switcher can never say one workspace while the view beside it acts on another,
and `navigate('workspace', ...)` makes the workspace it opens the active scope
for the same reason.
Member configuration is account-scoped and does not require a workspace.

The last selected workspace survives reopening. Browser and server-hosted
dashboards use the `aether.ui` local-storage preference. The local gateway also
stores it through `workspace.selection`, keyed by server address and member,
because the desktop app's ephemeral port changes the browser origin on each
launch. Startup restores this value before choosing a fallback; a selection
made while startup is loading takes precedence. A failed preference read shows
the gateway's original error in a toast without blocking workspace and run
data: the current valid selection or normal fallback still applies.

Derived data (the run groups, the flat run list, the Needs you counts) lives
in `src/store/selectors.ts` as pure functions over a `StateContext`
(`stateContextOf` in the same file), wrapped by memoizing hooks in
`src/store/hooks.ts`. Selectors that build new arrays must not be passed to
`useStore` directly. A view that owns its own derived shape keeps it beside
the view instead (`src/routes/board/selectors.ts`). `useRunInput` combines
structured native requests, pending Aether approvals and unanswered Run Room
questions for the request indicator; the run's state comes from
[Run state](#run-state).

**Slots** (`src/components/slots.tsx`). Where a route registry is too coarse -
something belongs *inside* a surface another ticket owns - the surface renders
`<Slot name="..." />` and contributors call
`registerSlot(name, id, Component)` at module scope. Registration order is
render order, `id` keys the render and makes a double registration an error.
The slots that exist:

| Slot | Props | Where it renders |
| --- | --- | --- |
| `card:badges` | `{ run }` | the run card's status row, alongside state and paused badges |
| `card:warnings` | `{ run }` | collapsed status-row controls beside Details |
| `card:chips` | `{ run }` | the card's Details disclosure, after the expanded run metadata |
| `card:footer` | `{ run }` | the bottom of the card's Details disclosure |

`AppShell` mounts the shell-wide hosts once: the command palette, the launch
and run forms, the shortcuts dialog, the updates dialog and the team refresh
(`useTeamRefresh`).

Card slot content may render its own links and buttons; the article's pointer
handler ignores interactive descendants, so those controls stay interactive.
Conflict chips and watcher avatars belong in these slots. The shared
request indicator includes approvals rather than adding a second badge.

## Sidebar

`src/components/shell/` is the shell: one sidebar and one content area. The
sidebar (`sidebar.tsx`) is a `nav` named "Aether", 260px wide by default and
resizable from 220px to 400px; older saved widths are clamped. `Mod+B`, the
**Hide sidebar** button in its header and Enter on its splitter hide it
completely; the content view's header then shows **Open sidebar** at its left
edge. Hiding or showing it hands focus to the control that reverses it. From
top to bottom:

1. **Workspace switcher and search.** The switcher
   (`workspace-switcher.tsx`) shows the active workspace's name and base
   branch, and the total Needs you count across every workspace. Its menu
   lists each workspace with its own count, **All workspaces** (the
   `overview` route), **Manage workspaces** and **Repository** (the
   workspace page). With one workspace and no `workspace.list` method it is a
   plain label. The search button opens the command palette.
2. **New run**, the only filled button in the shell (`n`).
3. **Runs** (`sidebar-runs.tsx`), a `region` with three groups, **Needs you**,
   **Working** and **Finished** (see [Run state](#run-state)), each an `h2`
   holding an `aria-expanded` disclosure button and its count. Finished starts
   collapsed. Needs you lists every workspace; a row outside the selected
   workspace leads with that workspace's name. Working and Finished list the
   selected workspace, and only the viewer's own runs while **Mine**
   (`mineOnly`, persisted) in the Working header is pressed.
4. **Navigation**: Board, Swarms, Activity, Files, Environment, Agents and
   Templates, then under a hairline Members (admins only) and Settings. The
   current one carries `aria-current="page"`. Gates come from
   `src/lib/surfaces.ts`, which also feeds the palette's Navigate group; its
   `palette` entries (Approvals, Agent config files, Devices, Manage
   workspaces, Onboarding) are reached from the palette only.
5. **One update notice row** when a CLI, server or desktop update exists, for
   example "Aether 0.5.3 is available · Update"; see
   [Update prompts](#update-prompts).
6. **Footer**: the member's avatar and name and a connection dot, whose word
   ("Live", "Reconnecting", "Offline") is in the button's name and tooltip.
   Its menu holds **Profile** (the personal sections of Members: account
   sharing and colour), **Keyboard shortcuts**, **Theme**, **Update…** when an
   update exists, and a **Team** line with who is online and the spend
   against workspace budgets.

A run row is a 28px `ListRow` (44px on a coarse pointer): a shaped state dot,
the title, and the agent's monochrome glyph. Its accessible name starts with
the state word, then the workspace when it is another one, the title and the
reason. Rows that do not need the viewer show their title in the muted colour.
A Needs you row offers its answer on hover, focus and a coarse pointer:
**Reply** for a question, **Review** for an unreviewed finish, otherwise
**Open**; each goes to the view the condition names. The list is one tab stop
(roving `tabindex`); Arrow keys, Home and End move within it, `j` and `k` move
from anywhere, and `u` opens the next run that needs you, oldest first.

A swarm is a mission whose integrator coordinates worker runs. It is one row
showing the objective and the workers' counts ("3 working · 1 needs you");
selecting it opens the swarm page. Only workers that need the viewer are
listed under it, and the row then sits in Needs you. Relationships come from
the run snapshot's `mission_id`, `mission_role` and `integrator_run_id`, not
task text. `mission.changed` coalesces background refreshes of those fields
without blocking run-status events or overwriting newer run state; reconnect
hydration supersedes pending relationship requests. Older servers that omit
the fields get a flat list.

**Phone (under 768px).** `top-bar.tsx` is a 48px `banner`, padded by
`--safe-top`: **Open sidebar** with an amber dot while anything needs you, the
view's title, **Search** and **New run**. A connection problem is one line
under it. The sidebar opens as a left side sheet, a `dialog` named "Aether"
with the same contents; a tap outside, Escape or any navigation closes it, and
navigation moves focus to the new view's heading. `Mod+B` closes it too,
because it is the key that opened it; every other shell key stands down while
it is open. On a phone the view's own header keeps only its actions, and its
`h1` stays for screen readers.

**Landmarks.** The skip link "Skip to content" is the first tab stop; the
sidebar is `nav` "Aether" containing `region` "Runs"; the content is the one
`main`, with one `h1` per view from `PaneHeader` (`tabIndex=-1`). After a route
change focus moves to that `h1`, unless the view took focus for itself. Unit
and end-to-end tests select by these names.

**Headers.** Every non-run view draws `ViewHeader`
(`src/components/view-header.tsx`), a `PaneHeader` with the title in
`text-title`, an optional one-line subtitle, the view's actions, the sidebar
opener while the sidebar is hidden, and on desktop the connection problem in
place of the subtitle.

### URL state

`src/lib/url-state.ts` keeps the route in the query string. A run view is
`?run=<id>`, with `&view=diff|browser|events` for the other tabs; any other
view is `?page=<name>`, plus `&id=<id>` for a swarm or workspace page; the
board is the bare address. The store starts on the route the address names
(`initialRoute()`), `bindRouteToUrl` pushes a history entry per navigation,
and back and forward navigate to the entry's route, so reload, back, `Esc`
and a shared link agree. Other query parameters and the hash are kept; the
token is removed on first load as before.

`aether://run/<id>` still works unchanged: both shells load
`<dashboard>?run=<id>`, which is that run's Terminal view. When the first
hydration does not find the run, the dashboard opens the board instead, or
onboarding for a member who has not finished it. These redirects replace the
history entry (`redirectRoute`), so back does not return to the dead link.

## Configuration view

The permanent `configuration` route renders the shared
`src/components/profile-import.tsx` importer. It is available whenever
`config.roots` and `config.import` are advertised, through both the local and
server-hosted gateways, without a workspace or onboarding prerequisite.
**Agents** provides a **Configuration** action, and `src/lib/surfaces.ts`
lists it in the command palette as **Agent config files**.
The local onboarding Agents step is an optional consumer of the same component.

The browser directory picker grants access to the directory the user selects
even when the dashboard is server-hosted; it does not grant access to arbitrary
local paths. The importer previews metadata and policy exclusions without
reading file bytes. It transfers the entire eligible directory through
sequential bounded requests, not a truncated selection. It retains at most the
current batch's encoded payload and shows cumulative progress. Files above the
64 MiB configuration-file ceiling and read failures are explicit errors.
Server exclusions and exact committed paths accumulate across batches; a
failure stops subsequent requests, distinguishing known commits from a request
whose response was lost.

After a result the user can select another directory or use **Open remote
files**, which navigates to the existing `files` route. There is no automatic
configuration sync, watcher, or import retry. Nonpersisted owner-scoped progress
and results survive navigation; `configImportPending` serializes operations and
protects against page unload. Each request rechecks identity after asynchronous
reads, so a member/server switch prevents further writes and hides the old
result. New browser-imported files use `0644`; existing
remote modes are preserved on overwrite. The browser cannot preserve source
executable bits or symlinks. See
[Agent configuration](harnesses.md#agent-configuration-import-and-files) for
the user workflow and [the protocol](local-gateway.md#files-and-member-configuration)
for import result and failure semantics.

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
editor renders complete UTF-8 text without NUL bytes up to 64 MiB; binary and
oversized responses remain read-only.

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
All runs the member launches and the environment terminal mount one shared
read-write persistent HOME, so accepted configuration imports and saves
are visible to active and future runs; a tool may need to reload. Browser
imports and Files edits do not create CLI snapshot history. Optional run
snapshot pins are provenance, not isolated writable copies. Manual profile
push and rollback overlay snapshot files into that same HOME without removing
paths absent from the snapshot; rollback is not an exact-tree restore.

## Window bar

The desktop window is frameless, so `src/components/shell/window-bar.tsx`
draws a 35px drag strip at its top edge whenever `window.aetherDesktop`
exists. On Windows and Linux it holds minimize, maximize/restore and close,
wired to `window.aetherDesktop.controls`; on macOS it is the strip the native
traffic lights sit in. The strip is `-webkit-app-region: drag` and its
buttons are `no-drag`. `App.tsx` mounts it above the `ConnectionError` page
too, so a total failure still lets the window move and close. A browser tab
has no bridge and no strip.

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

## Window size and overflow

The shell is a fixed row - the sidebar and the content view - under the
desktop window bar or the phone top bar, and nothing in its own chrome
scrolls sideways. A control pushed past an edge is unreachable, not merely off
screen, so every row states what gives way first.

`desktop/main.js` sets `minWidth: 960` and `minHeight: 600`. That is the size
the desktop rules are designed against; a browser tab has no such floor, so
the same rules degrade below it rather than break. A phone is the far end of
that: `src/app/layout.tsx` exports the viewport the shell needs there.

- `width=device-width, initial-scale=1` - the page is laid out at the device's
  own width rather than a desktop-sized canvas scaled down.
- `viewport-fit=cover` - the shell paints under the notch and the home
  indicator, and the chrome that touches those edges pads itself back out with
  `env(safe-area-inset-*)`: the phone top bar sideways and downwards, the
  sidebar sheet on all three edges it reaches, the sidebar on the left and the
  content view on the right and bottom. The top inset is the one a surface
  away from that edge also has to read, because an installed iOS app asks for
  a `black-translucent` status bar and gets the whole screen: it is
  `--safe-top` in `src/index.css`, and the top bar's height, the palette's
  drop and the side sheets' top padding all count it. A surface that pads
  itself keeps painting to the edge and insets only what it holds, so the
  notch shows the bar's own colour rather than a gap. Every inset is 0 where
  there is none, so nothing guards them. Toasts sit 8px above the bottom
  inset, and the offset is given to `sonner` twice, as `offset` and as
  `mobileOffset`, because `sonner` swaps to the second below 600px and
  otherwise falls back to its own default.
- `interactive-widget=resizes-content` - on a browser that honours it
  (Chrome and the Android WebView; iOS Safari does not), the soft keyboard
  shrinks the layout viewport instead of sliding the page under itself. That
  is what every `dvh` in the app - dialogs, the palette, selects, menus - is
  already sized against, so they all shorten when the
  keyboard opens. Nothing in the shell uses `vh`.
- `themeColor` per `prefers-color-scheme` - the browser reads it before the
  SPA has applied the member's stored theme, so it follows the OS scheme
  rather than the app setting.

A layout that only changes size belongs in CSS. Layouts that mount different
elements for a finger than for a mouse, such as the diff timeline's disclosure
and activity filter bar, ask `useMediaQuery` in `src/lib/hooks.ts`. Named
constants there include `coarsePointer`, Tailwind's `belowSm` breakpoint and
`phoneScreen`; the `md` edge is `MOBILE_MAX_WIDTH` and `useIsMobile()` in
`src/lib/breakpoints.ts`. Terminal utility controls respond to their
pane's container width rather than the window width. The shell has one
breakpoint: under 768px (`useIsMobile()`, Tailwind `max-md:`) the top bar and
the sidebar sheet replace the sidebar.

**Touch density is one variant, defined once.** `src/index.css` declares
`@custom-variant coarse (@media (pointer: coarse))`, and a control that a
finger has to hit carries its touch size beside its desktop one - for example
`size-[22px] coarse:size-11`. It answers for the primary pointer, so a touch
laptop with a trackpad keeps the desktop density. Under it the `Button`
sizes, `Input`, `CommandItem`, `MenuItem`, the `CollapsibleTrigger`,
the `Select` trigger and its options, the dialog close, the palette input,
the top bar, the sidebar run and navigation rows and the sidebar's own
buttons, the run-list title, the files tree rows and the approvals controls
grow to 40-44px, and the terminal toolbar row grows with the buttons in it.
Desktop density is untouched.
Use this variant rather than a new breakpoint or a per-component pixel value.

**The bars are tokens, not repeated numbers.** `--window-bar-height` (35px,
the desktop drag strip) and `--top-bar-height` (48px, the phone top bar,
border included) are declared in `src/index.css`, and every offset measured
from the top bar reads the token - the palette's drop, the phone Run Room and
evidence sheets. `--safe-top` is the top safe-area inset under a name, so that
a surface measuring from the top bar can add the same amount the bar itself
grew by. It carries a `0px` fallback because a bare `env()` in a browser
without it would void every `calc()` height that reads it.

Below `md` every centred dialog opens as a bottom sheet, full width and at
most 85dvh, and scrolls inside itself, so its footer stays in reach of a thumb
and the soft keyboard shortens it from the top. iOS Safari does not resize the
layout viewport for the keyboard, so `useKeyboardInset()`
(`web/src/lib/keyboard-inset.ts`) sets `--keyboard-inset` from
`visualViewport` and the sheet sits that far above the bottom edge; it is 0 on
browsers that resize. `md` is a width breakpoint, so
a desktop window narrower than 768px is treated as a phone here too.

- **Update prompts** live in the updates dialog, which scrolls inside itself;
  the sidebar carries one notice row. Technical output is bounded, and every
  prompt's actions sit on their own row so long diagnostics never hide them.
- **The sidebar** keeps the run list scrollable between its fixed header and
  its navigation rows and footer, from 220px wide up.
- **The run header** gives its first section two lines: the title, then state,
  harness/mode, the route's subtitle and **Task and details**. The title is the
  agent's last terminal title; a run without one uses its prompt's first line, cut at
  120 characters. The heading clamps to two lines and keeps the full label in
  its `title`; the disclosure keeps the full prompt and run metadata together.
  The second section holds the run-detail tabs and at most
  two labeled state-dependent actions plus **More**, at every width and for
  both pointer modes. Secondary actions live in More; Kill and Delete come
  last, after a separator, and require confirmation. Metadata and tabs
  scroll inside their own regions before the actions become unreachable.
- **The board** offers Cards and Map layouts. Cards stacks its three status
  columns on narrow screens and places them side by side from the
  `lg`/1024px breakpoint; Map pans and zooms inside a bounded canvas. Both
  keep state labels and run controls available (see [Board](#board)).

## Data flow

`connect()` in `src/store/sync.ts` owns the whole lifecycle. One round of HTTP
fetches hydrates the store (`server.info`, `workspace.list`, `member.list`,
`run.list`, `run.overlaps`, and `GET /api/v1/capabilities`), then `/ws/events`
is the only thing that changes it. `setWorkspaces` keeps a valid selection;
an unset selection or a workspace that has been deleted falls back to the
first by ID, or clears the selection when none remain. An open deleted
workspace route moves to the replacement workspace or **Manage workspaces**;
an open run in a deleted workspace returns to the board. The capabilities
fetch may fail without failing hydration; a legacy gateway then holds `null`.
The snapshot also seeds the board's paused map from each run's wire `paused`
field, skipping runs that do not carry it.

`removeWorkspace` records deleted IDs for the lifetime of the in-memory store.
Every workspace snapshot and upsert excludes those IDs, so an older route or
hydration response cannot resurrect a deletion received from another member.
Removal also repairs the selection and open route before any refresh awaits.

- **The subscription is established first.** Hydration starts only once the
  server acknowledges it (`{"ok":true}`), which is also when the client calls
  itself live. Otherwise a change between the snapshot and the subscription
  would fall in the gap and never be delivered or replayed.
- **Events arriving during a fetch wait in the queue**, and are applied once
  the snapshot lands, so an older snapshot never overwrites a newer event.
- **Events are applied one at a time, in sequence order**, each fully resolved
  before the next begins. The cursor is a single number, so it must never move
  past an event still waiting on a fetch.
- **Listeners hear one change per frame.** The queue drains inside
  `batchNotifications` (`src/store/batch.ts`): every `set()` applies at once,
  so `getState()` and the cursor rules above are unchanged, but subscribers -
  React included - are notified on the next animation frame (a 16 ms timer
  in a hidden tab). The hold outlives an emptied queue, because each socket
  message arrives in its own task and drains before the next one lands; a
  burst of 200 events spread over a few milliseconds renders once. A write
  outside the drain, such as a click, notifies at once.
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
  failed picks the copy: `network` says this machine has no network at all
  and names wifi and a VPN, `server` says the server did not answer through
  the selected transport, `gateway` says the local `aether gui` process
  stopped answering, `tailnet` says the phone got no answer from the server
  that serves it the page, and an access refusal preserves the gateway's own
  reason. A fetch that got no answer at all is `gateway` only on the desktop
  origin, where that process can be restarted; on the server gateway it is
  `tailnet`, because the page came over the tailnet and there is no local
  process to blame and no wifi advice to give. A local gateway linked through
  an edge classifies the client's own error text: `edge` (the edge did not
  answer), `edge-server` (the server is not connected to it), `signed-out`
  (no valid device token; gives the `aether login --edge` command),
  `device-revoked` and `device-pending`. `src/store/edge-sync.test.ts` pins
  the wording those classes match, so a change in `internal/edge/client` or
  the sshd banners fails there. The capabilities probe records
  which gateway serves the page before hydration runs, so the first failure
  is classified too. The gateway's message appears in an initially open
  "Technical details" disclosure, and the page suppresses the toast that
  would otherwise repeat it. Retry clears connection state and remounts the
  subscribe-and-hydrate cycle rather than reloading the page.
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
  does not. If an attach is still inside its replay boundary, `online` first
  cancels its serial parser and drain with an explicit cancellation signal,
  clears the partial operations, and drops the socket while keeping the
  terminal hidden. The replacement attach starts one fresh hidden replay, so
  no incomplete prefix can become visible and a stale completion cannot reveal
  the old host. A final replacement refusal settles the gate before showing the
  server's error. The event is rare enough that one resubscribe from `lastSeq`
  and one re-attach with its replay are the cheaper mistake. The 30 second cap
  stays for genuine outages, and an attach the gateway refused - or one parked
  on a `session ended` close, whose transcript cannot change again - is an
  answer rather than a failure, so neither event re-asks it.
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
  `run.title`, `run.protected` and `run.archived` follow the same fetch-first
  rule on a run the client has never seen; `run.controller` skips an unknown
  run, whose snapshot carries the holder.
  A `server.update` event lands in the `server` slice, which feeds the update
  prompts.
- **`run.agent` events set a run's `activity`** (`{verb, target, at}` on
  the run record, `src/store/activity.ts`), so a state line can say
  "Reading src/auth.ts" without opening a socket to the run. A tool call
  sets the present tense with the call's detail (file, command, task) or the
  tool name as the target; the tool result that follows turns it past tense
  ("Read", "Ran", "Edited") or `Failed`; a subagent reads "Delegating".
  Calls in flight are kept by `tool_use_id`, so a result ends the call it
  names: while another call still runs, the line shows the newest one. Only
  runs whose harness has an adapter emit these events, so `activity` is
  optional everywhere. An event about a run the client has not loaded is
  dropped rather than fetched, and a run re-read keeps its activity, since
  no snapshot carries it.

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
use them. Control calls use `POST /api/v1/<method>`. The diff patch, disk
gauge and capabilities descriptor are read through `GET`; development and
retained-evidence capture bytes also use an authenticated binary `GET`, not
base64 in control JSON. The shared browser's observation-only WebSocket is
`/ws/dev/browser/{run_id}`; actions stay on the typed HTTP control API.
`aether gui` sends its per-process token as
`Authorization: Bearer` on HTTP and as `?token=` on WebSockets. The
server-hosted gateway sends no token; WhoIs authenticates each request.

The disk gauge in Settings > Server renders when `server.info` carries a `disk`
object (`used_bytes`, `total_bytes`). That field does not arrive with
`server.info`: `protocol.ServerInfoResult` is shared with the CLI and frozen,
so the gateway serves the number on `GET /api/v1/disk` and the team reads
write it onto the stored info, which is the gauge's only reader. The field
stays optional and the gauge stays hidden if the read fails. What `statfs`
answers is the whole filesystem holding the data directory, not the directory
itself, and the gauge is labelled as that: it is the number that says whether
the box is running out of room, and claiming it as Aether's own usage would
be an invention.

`account.usage` is the quota RPC used by **Usage** in Settings > Server. Its params
are `{account_member_id?: string, refresh?: boolean}` and its result is
`{account_member_id, providers}` with independently decoded Claude/Codex
provider rows (`status`, `windows`, optional `plan`, `updated_at`, `retry_at`,
`error`, and `checked_at`). The provider endpoints are subscription services
whose response shapes can change; the server owns credentials, fixed provider
hosts, bounded fetches and cache policy. The dashboard does not refresh
tokens, run provider CLIs, or infer billing/history, and an older server that
does not know this method is shown as needing an update rather than polled
forever.

## Board

`src/routes/board/` is the default center view, reached through the
sidebar's **Board** row. Its header shows the run count and a **Cards / Map**
segmented layout control. The sidebar owns the primary New run action.

**Cards** arranges runs in the sidebar's three groups as columns, **Needs
you**, **Working** and **Finished**, with the same scoping, ordering and
Mine toggle (`board()` in `src/routes/board/selectors.ts` reads
`runGroups`). A swarm is one card with its workers' counts; workers never
appear as cards of their own. Each card shows its state chip and reason line.
The flat bordered
columns stack on narrow screens and sit side by side from the `lg`/1024px
breakpoint, where subgrid keeps their column headers the same height.

Cards uses compact, natural-height rows; Map uses fixed card geometry from
`map-layout.ts`. Both keep a bounded full-width title preview and the full
`runLabel` as the navigation button's accessible name. The state badge,
protection and archival indicators remain, without a redundant state dot or
New pill. Owner, harness and timestamp stay in the compact preview.
Counted file-overlap and mission-conflict buttons remain beside the status
metadata and open diagnostic popovers. **Details** reveals the full task,
reason, branch/copy control and additional metadata inline in Cards and in a
dialog in Map, so expansion does not disturb map geometry. The run page exposes
the full task and metadata through **Task and details**.

An empty workspace shows one "Ready for a task" panel and a primary New run
action rather than three repeated empty columns. Loading uses delayed
skeletons, and hydrated empty columns say "Nothing here." without confusing
an in-flight request with an empty result.

The card's article remains a pointer surface for noninteractive metadata, while
interactive descendants and any non-collapsed text selection are ignored by
the article handler. The Details content is navigation-exempt, so branch text
can be selected or copied without opening the run. It shows the full branch
with a copy button beside it.
Reaching for the branch is therefore not a way into the run; the rest of the
card is. Copying goes through `src/lib/clipboard.ts`, shared with
`CopyableCommand`, because an origin without `navigator.clipboard` - plain
http, an older engine - has to fall back to selecting the text for a manual
copy rather than failing quietly.

**Map** groups runs by their actual owning member, with a named boundary and
light identity tint. Standalone runs remain individual cards; swarm groups
label integrators and workers and draw directed hierarchy connectors. Workers
stay inside their own owner's boundary even when coordinated by another
member's integrator; those cross-owner connectors are dashed. A worker whose
integrator is not in the current map, for example because it is archived,
remains visible and is labeled as having an integrator not visible.
Relationships use the snapshot fields described under [Sidebar](#sidebar),
not task-text guesses. Deterministic rectangular shelf packing uses fixed
dimensions from `map-layout.ts`, not measured card-content heights, to arrange
the groups into a landscape-oriented map rather than a radial graph or a
single vertical stack. Zoom/Fit and archived-run controls share the Runs header.

Map navigation stays inside its canvas:

- Drag blank canvas with the mouse or scroll the wheel to pan.
- Hold Ctrl or Cmd while scrolling to zoom around the pointer.
- On touch screens, drag to pan and pinch to zoom.
- With the canvas focused, arrow keys pan; Shift increases the step.
  `+` / `-` zoom and `Home` / `0` fit all runs.
- Visible **Zoom out**, zoom percentage, **Zoom in** and **Fit** controls
  provide the same operations without gestures. Tab reaches run controls.

The selected layout and each workspace's map pan/zoom are stored in the
existing `aether.ui` origin-local preferences. They survive layout switches,
route changes and reloads; switching workspaces restores that workspace's
camera. Unlike the separately stored workspace selection, these preferences
do not cross origins, including a local gateway's changed ephemeral port.
While Map stays open, a changed run set or card geometry refits only when
every card would be offscreen. Routine metadata updates and return visits
preserve the camera.
Switching Cards to Map or back moves matching cards between their measured
rectangles, including width and height, over 460ms. Reduced-motion preference
skips this movement.

**Archiving hides a finished run from Finished without deleting it.** A run
carries `archived_at`/`deletes_at` once archived. Every hide guard -
`board()`, `runRows()`, and so every group and count - drops it once its
status is also final (`isArchivable`: `merged`, `abandoned`, `failed`,
`interrupted`); a route that opens a run by id is untouched, since it reads
the run map directly. In Cards, the Finished `ColumnHeader` grows an "Archived N"
toggle once N is over zero, swapping the column's content to those runs. Map
keeps the same toggle in its Runs header and replaces Finished runs with
archived runs while leaving active runs visible. Both return to Finished when
the last
archived run leaves. Each archived card, and the run header for one, show
`deletesInLabel(deletes_at)` (`src/lib/format.ts`):
"deleted today" under 24h (past due included), "deleted in 1 day" under
48h, then "deleted in N days".

**Archive closed runs... hides every eligible Finished run.** The Finished
`ColumnHeader` in Cards and the Runs header in Map offer this action; the
palette carries the same command whenever the gateway serves `run.archive`.
All open `ClearDoneConfirm` (`src/routes/board/clear-done-dialog.tsx`) over
the `clearDonePlan()` (`src/lib/commands.ts`) snapshot taken when opened.
Both actions read the active workspace's runs (`finishedRuns` and
`workspaceRuns` in `src/routes/board/selectors.ts`), whoever owns them: the
Mine toggle does not narrow them and another workspace's Needs you runs never
join them. When none
qualify, the dialog says "No closed runs to archive" and which runs archive
acts on. Eligible runs are
`isArchivable`, not already archived, and killable by the caller. The dialog
says archiving hides runs and schedules their deletion after retention, but
does not free container memory. It counts completed runs awaiting Close and
runs this caller cannot act on. `runClearDone()` archives at most six at once,
in Finished order (failures first, then latest change), starting another as
each settles. A failure does not stop the rest. `CodeNotFound` counts as
success and removes the vanished run locally. The final toast reports the
archived and failed counts, with the first real error in that order.

**Release finished resources... frees retained containers without archiving.**
The same Cards/Map header and the command palette offer workspace-scoped bulk
release; the palette offers it whenever the gateway serves `run.release`, and
its dialog says "No finished runs hold resources" and which runs release
acts on when none qualify. Its
`releaseFinishedPlan()` searches all runs in the active workspace,
including archived runs behind the toggle and finished runs still awaiting
review; it does not depend on visible cards. It requires `run.release`,
the Kill permission and a finished status with an existing retained-container
reason (explicit Close, agent report or mission worker). Active and
needs-attention runs are excluded. The confirmation says resources are removed,
relaunch becomes unavailable, and the run and history remain visible. The
bulk executor reuses the six-call concurrency bound, continues after failures
and reports successful and failed counts with the first server error. Unlike
bulk archive, a release refusal is never reclassified as a success; no release
removes or archives a run in the client. A same-status `run.status` event
updates its reason so the action disappears once its container is released.

### Run state

A run shows one of five states and one plain-words reason line, derived in
`src/lib/status.ts` (`presentRun`). The state is for the member looking: the
same run can need one member and read Working for another. The wire status
enum is unchanged; the run header's **Task and details** shows it as
**Lifecycle**. The run header also prints the server's `run.reason` under the
reason line whenever that line does not already contain it, so a parked
error or stall detail is never hidden. Both sit in a scrollable **State
reason** note that takes keyboard focus so a long reason can be scrolled.

| State | Wire status | Group |
| --- | --- | --- |
| Needs you | any, while a condition below applies to the viewer | Needs you |
| Working | `queued`, `provisioning`, `running`, `needs-attention` | Working |
| Paused | a live run paused by a member | Working |
| Done | `completed`, `merged`, `abandoned` ("Closed without merging") | Finished |
| Failed | `failed`, `interrupted` | Finished, sorted first |

Needs you means the viewer can resolve it. `src/lib/needs-you.ts` holds the
conditions as one table, checked in order; `needsYou(run, ctx)` returns the
first that applies:

| Condition | Who it needs | Reason line |
| --- | --- | --- |
| Pending permission (approval or native) | owner or terminal controller | Permission: … (enhanced: Permission requested) |
| Pending native question | owner or terminal controller | Question: answer in the terminal (enhanced: Question from the agent) |
| Queued message from another member | terminal controller | Bob sent a message, approve to deliver |
| Run Room question to the owner | owner | Bob asked you: … |
| Open swarm question | accountable human or an admin | The integrator asks: … |
| Integrator exited or failed to launch | accountable human or an admin | Integrator stopped, replace it to continue |
| Worker under a control hold | the member holding it | You hold control of worker 3 |
| Enhanced failure (`enhanced session failed: `, `enhanced session ended: ` or `enhanced turn failed: ` reason) | owner | Enhanced unavailable: … |
| Parked `blocked: <summary>` | owner; a worker's accountable human | Blocked: … / Worker blocked: … |
| Parked at `needs-attention` | owner | Agent idle for 3 min / No activity for 12 min |
| Unreviewed finish (`outcome_unseen`) | owner | Finished, review the result |

A condition that applies to someone else leaves the run Working with
"Waiting for Alice". A worker in a running swarm with a live integrator
counts only for a control hold or a blocked report: the integrator handles
its stops and requests. A stopped integrator counts only while the swarm
record names it as current; the dashboard loads the selected workspace's
swarms, so another workspace's stopped integrator appears once you select
that workspace. A Background (`headless`) run reaches Needs you only
through a pending Aether approval, the swarm rows, a blocked report, an
Enhanced failure or an unreviewed finish. A
Working run's reason is what its agent is doing ("Reading src/auth.ts", from
`run.agent`), else "Queued", "Starting" or "Agent working".

Needs you sorts oldest wait first (`waitingSince`: the approval's or
message's time, else the state change); Working and Finished sort by latest
change. Reason lines that name a wait re-read the shared clock, so "for 3
min" keeps moving. The run wire carries no status-change time, so the
dashboard records one from each `run.status` event. A run loaded from a
snapshot instead takes its finish, start or creation time
(`stateChangedAtEstimated`): its idle reason drops the duration ("Agent
idle") until the next status event, and it sorts and shows its change time
by that estimate.

**Terminal controller** comes from `controller_member_id`, which the
gateway decorates from the control lease on `run.get` and `run.list` (empty
when nobody holds it). A `run.controller` event, published whenever a lease
is taken, taken over, released, fenced or runs out its reconnect window,
keeps it current, so a teammate holding control of someone else's run sees
its requests without opening the Run Room. Only a gateway too old to send
the field falls back to the Run Room's cached status.

**Paused** comes from the `paused` field the gateway decorates from the
scheduler on `run.get` and `run.list`; a paused run still reads `running`.
The hydration snapshot seeds `pausedRuns` (`seedPaused`, skipping runs
without the field - a legacy gateway), and live `pause`/`resume` entries on
the `workspace.timeline` stream keep it current (`pausedFromTimeline` in
`src/store/board.ts`).

**Unreviewed finish** marks a run an agent finished with
`aether-internal report --outcome success|failure` (status `completed` or
`failed`) that its owner has not opened. The server owns the flag
(`outcome_unseen`, below), so it survives a reload and needs only the owner.
`watchOutcomeSeen` (`src/store/outcome-seen.ts`) calls `run.seen` when the
owner reveals the run through `navigate()`, or is already on it in a visible
tab when the flag arrives. It makes one call per reveal: a refusal shows the
server's error and is not retried until the owner opens the run again. The
run moves to Finished only when the `run.outcome_seen` event or the method
result clears the flag. Archive eligibility and finished-run checks read the
wire status, so they treat the run as finished throughout.

### Execution, input and paused on the wire

**The parked reason survives a fetch.** `protocol.Run` carries `reason` -
the last `run.status` reason, persisted with the run and sanitized
server-side - so a run that was already in needs-attention when the tab
loaded still says why: `waiting for your input` and its siblings when the
agent reported it, `stalled: ...` when the silence heuristic parked it.
`toRecord` in `src/store/runs.ts` prefers the wire reason and falls back to
the previously stored one only when the fetch omits it and the status has
not changed (a legacy gateway); a live `run.status` event still overwrites
it with the event payload's reason. A pending approval's action or unanswered
room question remains the card's actionable summary.

**Needs you is a presentation state, not a new lifecycle.** The
persisted/wire status remains `needs-attention`. Turn completion, silence,
failure, and prose such as the legacy `waiting for your input` reason are not
evidence of a request.

**Requests are independent of execution.** A run snapshot's `pending_inputs`
contains native request identities (`id`, `session_id`, and `kind`: `question`,
`permission`, `form`, or `extension_ui`), never prompt bodies or answers.
The durable `run.input` event replaces that set via `{pending_inputs: [...]}`;
an empty list clears it immediately. Closing one request leaves the others
visible. Run headers, cards, lists and sidebar rows combine this set with
pending Aether approvals and unanswered Run Room questions, including room
questions after execution finishes. Counts and tooltips name the source.
Use the existing Terminal for native prompts, Approvals for Aether approvals,
or open Run Room from Terminal for room questions; no new answer transport is
introduced. Unsupported native integrations show no inferred request.

A terminal lifecycle transition also clears native requests if its empty
`run.input` event is lost. Later native input events cannot revive a request
on a finished run. This does not clear Aether approvals or Run Room questions.

Hydration is authoritative and queues live events until its snapshot lands.
Ordinary run upserts preserve a known input set, including an empty one, so
an older route, room or launch response cannot resurrect a closed request.
A terminal run upsert clears native requests instead.
Mission relationship refreshes replace only relationship fields. Input events
for unknown runs follow the existing fetch-first and ordered-cursor rules.

**An unreviewed finish is server state.** `Run.outcome_unseen` on `run.get` and
`run.list` is true while an agent-reported outcome is unopened by the owner;
absent (an older gateway) means false. Every `run.status` event sets the flag
to its payload's `outcome_unseen`, the row's flag after that event - a
same-status re-label such as retention expiry included - so a later close or
relaunch clears it. A
`run.outcome_seen` event, or the Run `run.seen` returns, clears it. `run.seen`
is gated on `cap.hasMethod('run.seen')`, owner-only, and idempotent.

**Paused hydrates from the same snapshot.** With `paused` on the wire
(above), a reload shows Paused for a run paused earlier, and the
palette offers the right one of pause/resume. Against a legacy gateway
whose runs carry no `paused` field the state stays unknown until a live
`workspace.timeline` pause or resume arrives, and neither surface offers a
verb rather than offering the one the server would refuse.

## Commands: one list, two ways to reach it

`src/lib/commands.ts` holds run verbs (pause/resume, message, close,
kill, release retained resources, delete, archive/restore, protect/unprotect,
relaunch, pull branch, hand off) and board verbs (navigate, launch,
mark all seen, archive closed runs, release finished resources) as data:
an id, label, icon, capability gate and call. `useCommandRunner()` reports
gateway success or its real refusal in both the action bar and palette.
Deletion removes the confirmed run locally; archive, restore, protect and
release do not overwrite the server's events with an RPC response.

Archive/Restore are gated on `isArchivable(status)` (`src/store/runs.ts`;
`merged`, `abandoned`, `failed`, `interrupted`, never `completed`) plus the
kill permission and the `run.archive` capability; neither confirms, since
archiving is reversible. Final runs offer Archive as a primary action;
archived runs offer Restore. The palette resolves its focused run from the
run map by `route.params.runId` rather than the run list, so an
archived run's own page still offers Restore.

Release resources requires confirmation, `run.release`, the Kill permission
and a finished retained reason, regardless of archive visibility or whether
the run is a relaunchable TUI session. Released/expired runs no longer offer
Release, while history remains available. The server checks the lifecycle
again if a run changed after the command was displayed.

- **The command palette** (`src/components/palette/`) opens from **Search** in
  the sidebar or the phone top bar, `⌘K` on macOS or `Ctrl+K` elsewhere;
  `Cmd/Ctrl+Shift+P` remains an alias. It is mounted once by `AppShell`.
  Navigation comes before run actions, so opening the palette initially
  selects **Open the board**, not a mutation. With an empty query the
  **Runs** group lists the first 50 runs in group order, Needs you first; a query
  searches every run. A run matches on the label its row shows (its title,
  or the task's first line), branch, harness, workspace name and run ID,
  never the full task text.
  Opening a workspace also makes it the active scope. Run actions apply to
  the run named by `route.params.runId`, on any run-detail tab; the board has
  no focused run. The "Go to" group uses the gated `src/lib/surfaces.ts` list.
- **Visible buttons**, so nothing important is reachable only by a shortcut:
  New run in the sidebar and the notice an empty Board shows in place of its
  columns; destinations in the sidebar's navigation rows; and
  the run action bar (`src/components/run-actions.tsx`) in every run-detail
  header, with at most two contextual labeled actions plus **More**.
- **Appearance commands** are explicit: **Use system theme**, **Use light
  theme** and **Use dark theme** set the same persisted preference as Settings.
  They are available through every gateway; no cycling theme command is needed.

A `Command` carrying a `confirm` field—kill, delete and both close
actions—opens the same run-naming confirmation dialog from the header or
palette. Cancel is initially focused. Palette confirmations capture the
authenticated identity with the run and command; an identity change dismisses
pending or visible confirmation instead of applying it to another account.
The action bar locks while a verb is
in flight, showing a spinner on the running primary action or on **More**.
This also prevents a second click from racing a branch pull over SSH.
Primary buttons use the command's `short` label and its full sentence as a
tooltip; the overflow menu prints the full label.

The palette ranks with cmdk's default scorer. The pinned local cmdk patch
in `patches/cmdk@1.1.1.patch` stays because stock cmdk 1.1.1 leaves the
input's `aria-activedescendant` unset for the initial selection and stale
after filtering, and spreads caller props before that attribute, so no
wrapper can correct it. The patch also adds the list's `browseOrder`, which
restores current browse order when a query is cleared, and avoids redundant
scrolling and DOM reordering. Filtering and live data updates keep the
input focused and its active descendant tied to a visible enabled result.

hand off and protect need the run's owner or an admin. Before hydration the
caller's own record has not arrived, and the mirror answers yes rather than
making the shell's buttons appear a beat late. Pull is the exception that is
not a question for this policy at all: it is the desktop gateway fetching a
published run branch into the repository on this machine, so it answers to
`hasLocal('pull')` alone. It does not refresh a workspace base or authorize a
mirror source.

### The forms

The three verbs that need prose open a dialog rather than calling straight
through: launch, post a steer request to the agent, and launch from a template.
The message form is `inject-dialog.tsx` over `run.inject`; each submission
includes a caller-generated `idempotency_key`, which stays the same when the
request is retried and changes only after the message payload changes or the
submission succeeds. The server records that legacy method as a Run Room steer
request, so it follows the controller lease, 45-second queue, moderation, and
receipt rules. The launch and message forms are a store dialog
(`openPaletteDialog` on the `palette` slice) hosted by `AppShell` through
`components/palette/dialogs.tsx`, so a button on any surface opens one by asking
the store, with no dependence on the palette being on screen.
The template form's open state lives with
`CommandPalette` in `index.tsx` instead, because the store's dialog union
knows only the other two. It lists the active
workspace's templates over `template.list` and starts the run with
`template.launch` (both on `lib/api.ts` like every other call), then reveals
it.

The launch form asks for an account, a task, an agent and a mode. `account.list`
puts the caller first, followed by accounts explicitly shared with them. A
shared selection sends its ID as `account_member_id` on `agent.list` and
`run.launch`. `agent.list` still returns the caller's own agents and
installations, since the run executes in the caller's environment, and marks
with `login_missing` each agent the account's owner has no login for, with
`own_account_only` each name that resolves to the caller's own
member-defined agent, which runs only on the caller's own account, and with
`unavailable` the launch's own error for each agent whose owner login exists
but cannot be shared. The form takes the reason from those three fields,
never from `source`: a member-defined name that is also a server-wide
definition launches the server-wide one, so it can be `login_missing`. It
lists the first as disabled "(not logged in)" entries and says "<owner> is
not logged in to <agent>", that the owner logs in from the terminal dock on
their own Board, and to press **Refresh agents** then; the second as "(your
account only)" entries with "Your own agent definitions run only on your own
account" and which **Account** entry launches them; the third as
"(unavailable)" entries with "<agent> cannot launch on this account: " and
the server's error unchanged; the swarm grid disables their worker rows the same way and
drops a ticked worker whose agent stops being launchable when the lists
refresh. The task is optional in
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
caller's persistent `~/.local/bin` or, on a shared account, the owner's;
uninstalled shipped entries
remain visible on the Agents page so setup can install them. The launch form
also remembers the most recently used installed agent for each account and
falls back to the first installed entry. With nothing installed nothing is
preselected, so Launch stays disabled until the member picks one: "No agent is
installed in your environment." and a **Set up an agent** button sit beside the
field rather than replacing it, and the button opens the Agents view. On a
shared account the heading reads "Neither you nor <owner> has an agent
installed." and the note says the run uses the owner's login and the
caller's installation, or the owner's when the caller has none. A failed
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

`MenuContent` and `DialogContent` suppress the outline on themselves:
each takes focus programmatically when it opens and has nothing to show for
it. Their contents are not the same case. A `MenuItem` takes real DOM
focus under Radix's roving tabindex, so it wears the outline like any other
control, keeping its `focus:` background as well. A `SelectItem` is that case
again: Radix moves DOM focus onto the highlighted option, so it wears the
outline inset like a menu item, and `SelectContent` suppresses its own for the
reason the other two containers do. A `CommandItem` suppresses the outline
too, and that one is deliberate: cmdk never moves focus to it at all, leaving
it on the input and tracking the highlighted row with `aria-activedescendant`,
so a background is all it has, and all it needs. It is the one row the focus
sweep is told to skip.

**One keybinding table.** `src/lib/keybindings.ts` lists every shortcut as
`{ id, keys, scope, label, when? }`, with `keys` in
[tinykeys](https://github.com/jamiebuilds/tinykeys) syntax (`$mod` is Cmd on
Apple platforms and Ctrl elsewhere; a space separates the two presses of a
sequence). The table drives the handlers, the tooltips (`shortcutLabel(id)`)
and the shortcuts dialog (`?`, or **Keyboard shortcuts** in the sidebar
footer menu), which groups it by scope and adds the keys a focused tab strip, splitter or terminal
owns itself. `keybindings.test.ts` fails when two bindings in overlapping
scopes share keys, or one begins the other's sequence.

| Key | Scope | What it does |
| --- | --- | --- |
| `⌘K` / `Ctrl+K` | global | Open the command palette |
| `⌘Shift+P` / `Ctrl+Shift+P` | global | Open the command palette |
| `⌘B` / `Ctrl+B` | global | Show or hide the sidebar |
| `?` | global | Open the shortcuts dialog |
| `n` | global | Launch a run |
| `u` | global | Open the next run that needs you |
| `j` / `k` | global | Focus the next or previous run in the sidebar |
| `g` then `b`, `l`, `s`, `a`, `f`, `g`, `e`, `,` | global | Go to the board, all workspaces, swarms, activity, files, agents, environment, settings |
| `⌘Shift+M` / `Ctrl+Shift+M` | run | Toggle Run Room |
| `Esc` | run | Leave a run for the board |

A component answers its bindings with `useKeybindings(scope, handlers)`, which
pushes the scope onto the stack in `src/lib/key-scope.ts` while it is mounted.
The scopes are `global`, `run`, `request` and `composer`; `request` and
`composer` have no bindings yet. When a key matches in two live scopes, the
innermost wins. A binding without a handler does
nothing and is left out of the dialog: `n` is offered only to a member who
may launch, and a `g` destination only when the gateway serves it. A run's
keys are listed even from the board, where no run scope is on screen.

Two window listeners serve every scope. Chords (a modifier beyond Shift) are
matched while the event is capturing, so a terminal's own handler never turns
them into input. Single keys are matched while it bubbles, after Radix and any
component that acts on the key first have had their chance to mark it handled.

The single keys stand down whenever something else has the keyboard:
`keyboardBusy` and `inModal` in `src/lib/keys.ts` cover a text field or a
select, a live terminal or its focused history surface, an open menu or list
box, or an open dialog. A stray `n` typed at an agent has to reach the agent;
in history it does nothing. In a menu it is that menu's typeahead, and on a
select it jumps to the option that starts with it. The guard finds a select
by its `combobox` role, since the control is a button. **Settings >
Appearance > Single-key shortcuts** (on by default, persisted with the other
view preferences) turns off every character key in the table: `n`, `?` and
the `g` sequences. Escape and the chords stay live.
The `g` prefix waits 1.5s for the key that completes it, and any key that goes
somewhere else ends the wait.

A chord's own handler or its `when` decides where it stands down. The palette
chords cannot be mistaken for typing, and with the terminal holding the focus
and swallowing Tab they are the way out of a run. They stand down for a modal
rather than for anything that has the keyboard, through `inModal` and the
store flag that names the form the shell is hosting. They take the key from
the browser either way, so a stand-down cannot land the reader in the address
bar. `Mod+B` stands down like a single key, so a terminal keeps Ctrl+B for
tmux.

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
Escape is the exception. Radix closes the tooltip and marks that press
handled, so the shell ignores it: on a run, the first Escape closes the
tooltip and the second leaves. Every other key reaches the shell as usual, and a
tooltip closes on the first of them whatever it is, so a pending `g` is
untouched.

Blocking a control with `aria-disabled` rather than `disabled` keeps it in the
tab order, which is the point; the Styleguide rule below says why. The run
action bar, its overflow trigger, the terminal toolbar and the diff snapshot
list all keep their tab stops while their verbs are unavailable, and each
guards its own handler rather than relying on the browser.

**The modifier is named after the reader's keyboard.** `$mod` is Cmd on Apple
platforms and Ctrl elsewhere, read from `navigator.platform` as tinykeys does,
and `formatKeys` in `src/lib/keybindings.ts` prints the same side: the palette
badge reads `⌘K` on macOS and `Ctrl+K` elsewhere. Terminal zoom belongs to
xterm, not the table, and accepts Ctrl and Meta alike. Terminal copy, paste and
find are Ctrl on every platform, because that is what xterm binds; see
[terminal.md](terminal.md).

**Tab strips behave as tab lists.** The run-detail strip (`tabs.tsx`) and both
docks (`components/dock.tsx`) carry `role="tablist"`, `aria-selected`, a
single tab stop that follows focus, and Left/Right/Home/End through
`onTabListKeyDown` in `src/lib/keys.ts`. A removable dock tab advertises
unmodified Delete and Backspace through `aria-keyshortcuts`.

Neither strip is a Radix `Tabs`, though the library is already a dependency.
The run strip cannot be: its five tabs are separate registry routes with no
common parent to hold a `Tabs.Root`, and Radix would emit `aria-controls`
pointing at panels that are not in the tree. The dock keeps each tab's close
affordance inside its native tab button instead of nesting another button
inside the tab list; pointer closing stops that tab's activation. What is left
of the pattern either way is `onTabListKeyDown`, one function both strips share.

Those keys move focus and nothing else. Selection does not follow focus here,
which the ARIA tab list pattern reserves for panels that are cheap to swap:
behind these tabs are a websocket attach, a patch fetch and an xterm host that
replays a transcript, so arrowing from Terminal to Events must not open the tab
it lands on. Enter or Space opens the focused tab, a click opens the tab it
landed on, and both work because every tab is a real `<button>`.
Delete or Backspace closes the focused removable dock tab. A close repairs
focus to the next surviving tab, the previous one when closing the last tab, or
the Add terminal tab when the dock becomes empty.
Each run route names the body under the strip as its `tabpanel`, through
`runTabPanel` in `tabs.tsx`, and in both strips the selected tab is the only
one carrying `aria-controls`: on the run strip only one of the four routes is
active at a time, so the other three would be naming a panel that is not active
in the tree. An open dock's body is the panel its selected tab names; a shut
dock has no body, and a dock
holding no tabs is no tab list at all, so neither names anything.

Opening a run tab replaces its header and strip with the active center view's.
The new strip restores keyboard-activation focus so it does not fall to `body`.
That handoff is armed from the activation rather than the key press, and only
from one the keyboard produced, which carries no click count: a cancelled
press arms nothing. Pointer navigation does not steal focus; it scrolls the
selected tab into view so the run actions cannot leave its label clipped.
The dock's strip needs no handoff because its own tabs do not unmount it.

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
tab strip (`tabs.tsx`), so Terminal, Browser, Diff and Events are registry
routes on the same `runId`; the strip is a real tab list, arrow keys included
(see [Keyboard and focus](#keyboard-and-focus)).

Every way into a run navigates to `terminal`, because that is where the agent
is: board card, sidebar row, run list, palette, feed entry, approval,
conflict chip, template, and the launch and onboarding forms. The shared
`RunHeader` keeps metadata with those four tabs rather than a per-run Overview:
**Task and details** contains the full task, owner, borrowed agent account,
created and changed times, and last commit when its timestamp is present.
State, harness/mode and the route's subtitle remain in the compact header;
the branch remains a subtitle where supplied, while protection and archival
expiry stay by the title. The second section holds the tab strip and actions.
The run's reason remains readable in the shared header.
Each active run-detail route renders one `RunHeader`; a parked terminal keeps
only its primary pane and therefore no header/tab IDs or action portals. This
lets the run's own state travel with the reader, and `isRunRoute` in `tabs.tsx`
is what keeps a sidebar
row lit while they move between the tabs. The disclosure keeps long task text
available without enlarging the compact summary.

### Run Room and control lease

`RunRoom` is the collaboration view for the current run. Its collapsed opener
sits beside the terminal; opening loads the durable room timeline, while
presence refreshes even when collapsed. The count comes from the collaboration
slice's unanswered questions and queued steers.

The server-side lease, reconnect, takeover, steering, protection, attachment,
and question contract is defined once in
[Run control and the Run Room](terminal.md#run-control-and-the-run-room).
Dashboard changes must preserve that contract rather than restating it here.

The dashboard-specific state wiring is:

- `RunRoom` reads history, pagination, loading, action errors and presence from
  `src/store/collaboration.ts`. `run.room.list` loads on opening and reconciles
  every ten seconds while open; live room events merge through normal store
  sync. `run.room.status` runs on mount, every ten seconds and when acknowledged
  control metadata changes. Refreshes are serialized with an abortable 15-second
  deadline; polling continues after timeout. Each snapshot retains the exact
  acknowledged `ControlMetadata` reference: taking then releasing can restore
  the same generation, so generation values alone cannot establish freshness.
  A changed reference marks the cached controller **(last known)** until the
  next response. Late responses from an old scope or timed-out request are
  ignored. Presence errors stay separate from history errors: retained names
  are marked stale, and unavailable/loading status is not shown as nobody.
- The composer calls `run.room.post` for comments, questions, replies, and
  steer requests. Approval and denial call `run.room.decide` with the control
  metadata supplied by the terminal attach.
- `TerminalView` owns the attach callbacks and passes the current
  `ControlMetadata` to `RunRoom`. `RunDock` keeps shell control state beside
  the agent terminal and exposes its own control action without duplicating
  room state.
  While the run is open in a visible tab, `RunDock` reads `dev.terminal.list`
  and the active tab's `dev.control.status` together every ten seconds; its
  own start, stop and control actions refresh at once, so the interval only
  catches what other members and the agent did.
  The toolbar and phone Room share `ControlButton` and `useTakeover`; the
  host renders one `TakeoverDialog` above Room and Evidence for the current
  holder's decision. `RunDock` defers Evidence's responsive sheet/popover swap
  during that decision to keep its focus scope stable. The server owns
  transfer timing and authority, and the dialog restores the interrupted
  terminal or composer focus. Attachment reopens still invalidate the old
  decision. Shell and Browser control remain independent leases, not
  authority over the agent terminal.
- `TerminalRoute` excludes `run.mission_role === 'worker'` from desktop owner
  automatic write requests. Opening a subsession therefore starts as a mirror,
  not a human takeover. `useRunTerminalSession` still gives deliberate per-run
  write intent precedence over that default, with its existing identity and
  authority fences; Take control and Release use acknowledged control frames.
  Ordinary/integrator owner defaults and phone mirrors are unchanged.
- **Ctrl/Cmd+Shift+M** toggles the room from xterm or the composer; the opener
  tooltip, room header and shortcut reference show the platform-specific key.
  The capture handler prevents terminal bytes, focuses the composer on keyboard
  opening and restores the invoker (or opener) on closing without clearing the
  draft, mode or attachments. Dialogs, the palette, composition and already
  handled events take precedence.
- **Comment** starts selected with a neutral selected segment and a
  collaborators-only hint. **Send to agent** uses teal and explains the queued
  instruction and controller approval consequence; colour is not the sole cue.

On desktop the open Room is a real flex sibling beside the terminal and dock,
below the run header, capped at 420px and 40% of the available row. It does not
overlay the terminal or its controls. The terminal toolbar owns same-run
controller/presence facts and the header owns protection; Room does not repeat
them on desktop. On a phone Room is a full-width modal sheet below the
top bar, retaining those contextual facts and containing keyboard focus.
Escape or the Room shortcut closes it and restores focus without discarding
the draft. Both surfaces share server state: questions and queued steers
contribute to the Run Room count; an unanswered question to the owner also
puts the run in the owner's **Needs you** without creating another inbox.

The Terminal tab's single **Evidence** trigger lives in the dock header through
`Dock.persistentActions`, including when the shell is collapsed or has no
tabs. Room does not duplicate it. On desktop Evidence is an anchored Popover
bounded by the terminal tabpanel; on a phone it is a modal sheet.
Capture selections, verification notes, candidate drafts and pending mutations
belong to the open Evidence session, so crossing the phone breakpoint does not
discard them or create a new retry key. Explicitly closing Evidence clears
local drafts; it does not cancel a mutation already submitted to the server.
**Answer with fact** closes Evidence and opens/focuses a Room comment draft.
Each fresh request object is consumed once, preserves attachments and clears
question correlation; it never sends automatically. Source availability,
expiry, partial results and retained-evidence authority remain unchanged.

### Candidate review in Run evidence

The existing `EvidenceDrawer` is also the candidate review surface; candidate
state is not a second run board. Its retained packet view keeps the heading
**Recorded observations, not verification**, and the candidate panel labels raw
packet snapshots as **Raw packet — observation, not verification**. **Review
candidates** opens the candidate list for the current workspace, and
**Prepare candidate** starts a review from ordered retained packets. **Add
selected packet** adds another exact source; the preparation action remains
**Prepare candidate**. **Refresh candidates** re-reads the list and
**Show candidate** loads the selected aggregate.

The evidence drawer uses viewport-fixed positioning on desktop and phone so
its controls are not clipped by the room or terminal's scroll containers.
Its body scrolls within the available viewport height.

The review shows the exact ordered inputs, their observation snapshots, target
and expected revision, candidate revision/state, conflicts and file
resolutions. **Apply resolutions** submits explicit path edits or deletes and
continues isolated assembly. Once frozen, the panel shows the exact argv,
observed image, runtime identity and working directory, bounded resource and
timeout details, setup/environment provenance, result, bounded output and
provenance for each verification. **Run verification** starts the server-side
check;
**Request delivery** binds the selected verification IDs, target, expected
revision, and action; **Approve delivery** or **Deny delivery** records the
human decision, except on a request a swarm's integrator made, which arrives
already approved; and **Deliver approved** executes the already-approved exact
request.

Candidate mutations are disabled while the connection is **Offline**,
**Reconnecting**, or **Connecting**. A return to **Live** refetches candidates
and the selected review before enabling controls, so stale revisions and
requests are not reused. A refreshed candidate with a new identity or version
invalidates resolution drafts; an own partial **Apply resolutions** response
keeps only untouched drafts that still conflict at the same assembly step.
The panel preserves the gateway's real error rather than manufacturing a
client-side result. Candidate review has no separate attention board or agent
decision path; it remains an evidence-linked human review flow.

The Missions page reuses the same panel with `readOnly`: a swarm's integrator
prepares, verifies, and delivers on its own, so the page lists the
workspace's candidates and **Show full** loads one, with its state, inputs,
conflict paths, verification results, delivery request, and receipt. Packet
selection, target fields, conflict editors, **Prepare candidate**, **Run
verification**, **Request delivery**, **Approve delivery**, **Deny delivery**,
and **Deliver candidate** are not rendered.

The wire methods and bounded records are documented in
[integration.md](integration.md); this guide records only the dashboard
surface and its reconnect behavior.

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

`CenterView` mounts only the active route. A terminal key includes the route,
the authenticated identity, the terminal data-generation epoch, and the run id,
so a change of identity or epoch remounts the surface instead of reusing it.
Leaving the terminal closes its WebSocket and disposes xterm; no hidden live
terminal stays warm. A pinned view retains only its static presentation and
history state. Returning attaches again at the compact current screen behind
the saved reading surface, without replacing its content or position. A run
left following live output shows that fresh current screen. Neither path
replays the retained archive into xterm.

The normal run xterm requests up to 5,000 scrollback rows. Once the server
acknowledges geometry, xterm adapts the combined normal and alternate buffers
to stay near 1,000,000 cells; wider terminals therefore retain fewer rows.
That bound is the live surface. `history.tsx` integrates older recorded output
into upward scrolling in the same pane: normal-buffer wheel-up, `PageUp`,
`Home`, scrollbar movement or a downward finger drag freezes the current
presentation and enters reading mode. Ordinary alternate-screen gestures
remain app-owned; `Shift+PageUp` explicitly enters recorded output there,
using a prior captured normal screen if available rather than pretending the
alternate screen is normal scrollback.

The reading surface virtualizes visible rows plus overscan and prefetches near
the oldest loaded rows. Its bounded scroll coordinate window shifts around the
reader for very large archives; it does not discard older pages or impose a
fixed line-count cutoff. Archived rows have stable negative indices, with
the newest at `-1`, and retain their opaque server cursors. Frozen normal-screen
rows have nonnegative indices. An explicit inline boundary separates normalized
recorded text above from frozen VT presentation below. There is no heuristic
text deduplication across that boundary and no claim of exact historical VT
reconstruction.

`history-cache.ts` stores pages, continuation metadata and the frozen HTML view
in IndexedDB, with an eight-page resident text LRU. The saved anchor is a stable
row plus its relative pixel offset and horizontal offset, not a distance from
the ever-changing live bottom. Prepending pages and switching A to B to A
preserve that anchor and the same cursor-linked row while output continues.
The frozen rows keep their captured layout through live geometry changes;
shared font zoom scales their presentation without rewrapping them.
Storage is scoped by authenticated identity, terminal-data epoch, run id and
creation time. Identity/epoch changes, authoritative run deletion and stale
async completions cannot restore another scope's data. Leaving cancels fetching,
not the saved view. Browser-storage failures are visible; unavailable storage
allows an in-memory session fallback, not a durable-restore guarantee. Missing
persisted pages are errors, not silent truncation.

Arrows, `PageUp`/`PageDown`, `Home`, wheel and touch browse the read surface.
Scrolling downward to its bottom or pressing `End` returns live. Only a new
reading episode after returning live resets paging to the newest archive
head; remounting a pinned run keeps its continuation and loaded pages.
Empty bounded scan windows continue automatically while yielding to input;
genuine fetch/storage failures appear inline. Existing pane Find searches
retained loaded pages and frozen rows, not unfetched server history; copy
selects from the read surface. Typing, paste and image insertion stay muted
while reading. The hidden native host is inert; input guards block user
actions without suppressing authorized terminal-generated protocol replies.

A same-incarnation resume, when the current surface is still mounted, supplies
only the bounded gap. An invalid cursor, ring, geometry, or incarnation falls
back to a compact current-screen bootstrap through the hidden serial
transaction. A finished run stays read-only until that same run is relaunched.

Below the shared run header, the Terminal view places the agent terminal and
`RunDock` in one flexible column, with the desktop `RunRoom` beside that column.
The agent terminal keeps flexible space above the dock. The first
header section contains the title and metadata on two lines; the next contains
tabs and actions on one line. The existing terminal toolbar contains connection,
control and presence alongside its tools, with no extra viewer/controller row.
It names the controller and all viewers; the viewer list scrolls horizontally,
and **Terminal tools** takes utility actions in narrow or touch panes. **(this tab)** requires
live acknowledged local control; **(another session)** distinguishes the same
member's other controller session. Real gateway errors remain readable.
At narrow widths key and eye icons identify the controller and viewers; full
role labels stay accessible, with the session marker also in the controller's
hover title.
The viewer scroller is keyboard-focusable.
Find opens in a temporary row below the strip so it never obscures a matched
terminal line. Closing Find returns that space to the terminal.

The dock header uses a `min-h-9` strip rather than a fixed 40px height. It can
wrap actions below the tabs on narrow screens, while the tab list scrolls
horizontally. Add and collapse controls stay keyboard and pointer reachable;
the close affordance is pointer reachable inside each removable tab, and its
Delete/Backspace shortcut is available while that tab has focus. The splitter
is reachable too. The shell tab strip is a custom manual tab list with one
keyboard stop and overflow scrolling; it does not use a component-level tab
primitive.

`TerminalPane` lays out its shared toolbar and optional Find row above the
terminal host. During a dashboard run's compact current-screen
bootstrap it receives `replaying={replaying}`: the xterm host is hidden with
CSS visibility while each frame-sized operation is parsed through one serial
xterm write chain. When no saved reading surface covers it, the pane says
**Restoring terminal history** throughout the parse, paint delay, and
saved-viewport restoration; the status clears only when the surface is ready
to reveal. It describes compact bootstrap restoration, not an archive scan.
When a pinned view exists it remains visible instead. The run terminal,
run-shell tabs, and environment dock all use this shared replay gate. User
input and terminal-generated replies remain muted through the final replay
write callback. That callback restores authorized protocol replies, including
while reading; user input remains blocked until returning live. A full replay
remains hidden for two paint turns, then its queued viewport restoration
settles before visibility is restored.

There is one vertical scroll owner at a time: xterm while live, the virtual
read surface while pinned. The host and ancestors suppress competing vertical
overflow. xterm's scrollbar inherits the host's CSS visibility: its
`visible`/`invisible` classes control opacity and must not pick up Tailwind's
visibility utilities. Otherwise an inert live scrollbar remains painted over
the reading surface or compact-bootstrap overlay.

Live xterm follows output at the bottom. Its controller still
protects viewport intent across structural replay or column reflow: it restores
only a current operation, with no intervening viewport interaction and no
normal/alternate-buffer change. The run's saved static reading surface is
independent of those live-buffer operations, so a fresh bootstrap cannot
overwrite it or shift its anchor.

The [scrollbar comparison](media/terminal-scrollbar-visibility.webp) shows,
top to bottom, the leaked scrollbar, the corrected reading surface, and the
return to the live prompt. It uses the real terminal components with synthetic
output; the headless browser suppresses its own native scrollbar in captures.

`TerminalTools` in the same module owns the search, zoom/reset, copy,
copy-last-screen, paste, and `TerminalImageAction` controls; the run terminal
supplies connection, control and presence in that same toolbar.
Fine-pointer panes show the tools inline from 42rem without attachment controls
or 70rem with them; narrower panes and touch use the compact **Terminal tools**
popover. The shell strip keeps one lease control plus **More** for Screenshot,
Hide terminal and confirmed Stop terminal. These actions belong to the
selected shell, not a global terminal lease.
Terminal tools delegate key behavior to the xterm controller and clipboard
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

**Shared terminal geometry.** Every terminal renders the server's acknowledged
grid and subsequent `geometry` frames, including desktop writers whose panes
are larger than another writer's. `useXterm.geometry()` returns the local pane
measurement from FitAddon without resizing the renderer; `setGeometry()` queues
the server resize and optional attach reset between xterm writes. A font or
pane resize reports the local measurement, never the smaller rendered grid,
so a viewer cannot accidentally pin the shared minimum after it grows.
Queued geometry work is discarded when its terminal is disposed.
The server queues geometry with terminal output. SSH and in-process dashboard
attachments request ordered framing, and the gateway emits the geometry frame
before the next output record; independent geometry and output pumps would
allow a repaint to reach xterm at the old size.

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

- sends `follow` in the attach header. `useXterm`'s `follow` option suppresses
  local measurements and resize reports, while `setGeometry()` applies the
  server's grid just as on desktop. The header carries `standardGeometry`
  (80x24) only for a session being created, such as a new shell tab.
  On phones the live terminal host exposes both axes. `useTerminalPan`
  reveals the cursor on entry, focus, input and viewport changes, without
  resizing xterm; a manual pan pauses following until a tap, focus or input resumes it.
  It rounds fractional viewport bounds inward so the whole cursor cell remains visible.
  History capture keeps the absolute outer pan offset separate from
  accumulated gesture deltas, including when disposal beats the next paint.
  Layout cleanup records the latest pan before host detachment, even if its
  scroll event has not fired.
  The integrated run-history surface owns both axes while reading, with
  live-grid panning disabled. Steering still keeps this viewer out of the
  shared size calculation.
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
than spending it on the wrong byte. On phones, and when the run toolbar is too
narrow for inline tools alongside presence, **Terminal tools** opens a bounded,
scrollable popover with named search, text-size, copy, paste and upload actions
instead of a second permanent toolbar row. Wider layouts keep inline tools.

In a run's normal buffer, a downward finger drag pans the live grid to its
top before handing off continuously to integrated history. Horizontal drags
do not enter history. Subsequent history swipes browse older pages or return
live at the bottom. Panning either surface does not resize the PTY or raise
an input keyboard. Oversized alternate screens use live-grid panning too;
when the grid fits vertically, application scrolling remains native. Shell
and environment terminals retain native xterm scrollback.

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
collapsed, but `persistentActions` keeps Evidence reachable even with no shell.
`TerminalDock` mounted with `openOnMount` expands itself once, because the
Agents and GitHub steps type into it.

Run-shell tab state and its socket registry live in `src/store/terminal.ts`;
the environment dock has the corresponding state and socket registry in
`src/store/env-terminal.ts`. The live attachment objects stay outside
persisted Zustand state, but a dock socket can outlive the component that last
displayed it. When a new xterm host adopts one, the dock calls
`Attachment.rebind()` with fresh callbacks. `rebind(next)` is a
host-replacement boundary: it cancels any old replay parser or drain with an
explicit cancellation signal, drops the old socket, updates the handlers, and
starts one fresh full replay for the shell or environment dock after
cancellation. Docks must not separately call `reopen()` after `rebind()`. Run
shell callbacks guard the current
`{ runID, tab }`; the environment dock guards its current tab. Host
subscriptions are removed on cleanup, while closing a tab or an exited shell
unregisters its socket. Thus route changes and tab remounts cannot deliver late
output, resizes, or image actions to a disposed host; only the selected shell
tab mounts an xterm host and transcript replay restores its content.
The primary agent attach closes when its route unmounts. A return visit
opens a new compact current-screen attach rather than revealing a retained
terminal.

The board's `TerminalDock` exposes **Save environment** as its primary action
while the member's terminal is running. **More** contains capability-gated
**Forward port**, **Stop environment** and **Reset to standard**. Stopping the
container and discarding the saved image remain separate decisions with
separate confirmations: Stop confirms with a plain primary button and says
the saved image is kept. Reset appears only when there is a `saved_image` to
throw away, confirms with the image's own name and carries the only destructive
button either dialog has. That confirmation
names the container only while there is one to stop, because Reset outlives
it. Stopping therefore carries the rest of the status forward rather than
replacing it, so the image survives the container in what the dock knows as
well as on the server, and Reset stays on offer with the environment stopped.
Closing a dialog opened from **More** returns focus to that menu trigger, or
to **Open** if stopping or resetting removed the menu.
When the terminal is running and `saved_image` is empty, it shows the hint
**Installs here reach agents after you save.** From the moment a tab opens until
its attach is acked, a spinner covers the terminal. Once a dashboard run ack
declares a compact bootstrap, the xterm host remains hidden with CSS visibility
and the pane says **Restoring terminal history** while each frame-sized
operation is parsed serially as it arrives. Input and terminal-generated
replies open only after the final replay write settles; the host remains hidden
through the paint delay and viewport restoration described above. Run-shell
tabs and the environment dock use the same settled-surface gate for their own
stream mode. A zero-length replay has no bootstrap bytes or paint delay; any
saved-viewport restoration still settles before the host is revealed.
The status words follow what
the dock knows: a terminal it has not seen running is **Starting your
environment container**, which is the wait Docker's container start accounts
for; a second tab, a tab switch or an expanded dock is **Connecting to your
environment**, with no container to start. A refused or failed start replaces
the terminal with the gateway's own error instead.

The same stop confirmation (`StopEnvironmentDialog`) backs the Members
page's notice after a member's first account share: containers started
before that share lack the in-place mount of Claude Code's login file
([security.md](security.md#account-sharing)), so when the store's
`envTerminal.status`, re-read with `terminal.status` at the share, says the
terminal is running, or that re-read fails, **Your agent account** says so
(that the terminal could not be checked, on a failed read) and offers **Stop
environment**. A share while another grant already exists shows nothing,
because the page cannot tell whether the terminal predates the first one.

- **The socket is `attach.ts`**, framework-free. It reuses `backoff()` from
  `src/lib/stream.ts`, splits paste input below the gateway's 64 KiB frame cap,
  and keeps callbacks bound to the current terminal host. The primary run
  header requests `screen:true` and `interactive:true`; shells and CLI
  attachments keep their existing stream modes. `screen:true` bootstraps the
  compact current screen and bounded scrollback. Upward scrolling fetches older
  normalized output through `terminal.history`, independently of the attach.
  The dashboard does not download the raw archive.
- **Controller lease and compact bootstrap.** A desktop owner's first attach
  asks for write for ordinary and integrator runs, not mission workers.
  Mission-worker terminals and other members start as mirrors. The server
  grants write only when no controller exists; a second tab cannot become a
  second writer.
  A mirror uses the toolbar's controller/viewer names and **Take control**, not
  a repeated read-only instruction. Live, acknowledged local control is shown by
  the toolbar's **(this tab)** controller marker and **Release** action, plus a
  steady 1px teal inset outline around the terminal while input is writable.
  The pointer-transparent outline uses `--primary`, the **Release** button's
  fill, in both themes. It never changes layout or animates, and stays
  identical under reduced motion. It is absent
  during replay or history reading, and on release, mirroring, disconnect or
  denied steering.
  Starting runs keep their spinner; ended runs say **This run is not running**.
  Whether a member may steer is the server's answer:
  `-32001` downgrades the attach to a mirror and disables the toggle. An
  occupied write request is a conflict and needs the host's shared confirmed
  takeover.

  The attach ack's `replay` count is the exact bootstrap/live byte boundary,
  even when a WebSocket frame straddles it. For a fresh, live, or finished
  dashboard run attach, those bytes are the compact current-screen snapshot,
  not the complete retained transcript. A valid same-incarnation
  `resume`/`cursor`/`resume_id` supplies only the bounded gap and keeps the
  warm screen. If the cursor or ring cannot serve it, `resumed:false` selects
  a compact snapshot fallback. `connectAttach` parses frame-sized operations
  through one serial xterm write chain. Input and terminal-generated replies
  remain muted through the final write callback; input can then open unless
  the user is reading history. The host stays hidden through two animation
  frames and structural viewport restoration, and remains behind a pinned
  reading surface until return-live. The attach never allocates a
  transcript-sized browser buffer.

  **Control changes stay on this WebSocket.** The client sends
  `{"type":"control","request_id":17,"write":true,"takeover":true,
  "control_generation":8}` (omit `takeover` unless explicitly displacing a
  controller). The ordered response has `type:"control"`, the same
  `request_id`, `ok`, optional `code`/`error`, and authoritative
  `has_control`, `control_session_id`, and `control_generation`. An
  unsolicited lease or **Steer** revocation has no `request_id`; it carries
  `ok:false`, `has_control:false`, and the exact revoked generation. An
  interactive attach remains open as a read-only mirror, with no replay or
  reconnect. Input carries the current `control_generation`; stale input is
  fenced. Take and release do not reconnect or replay, and the UI changes its
  writable state only from acknowledged metadata.
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
  the provisioning failure, so the tab shows that reason directly alongside the
  shared run header. That substitution is keyed on the
  refusal's own code: only a missing session says anything about the run, so
  a revoked token or a withdrawn membership still shows the gateway's own
  message.
- **Run-shell tabs request the controller lease.** The `+` control opens names
  `t1`, `t2`, `t3`, and `t4`; four is the per-run limit, six is the environment
  dock's. Each shell attach requires Steer and asks for the same one controller
  lease, so an occupied shell request is refused rather than becoming another
  writer. Each uses `/ws/attach/<run>?shell=<tab>` and closes its socket when
  the tab is closed. `RunDock` exposes an uploaded-image path only while its
  attached identity still matches the current `{ runID, tab }`. A `-32001`
  response does not reconnect; the dock replaces the terminal with **You can
  view this run but not open a shell in it**. A normal `1000` socket close
  removes the finished tab.

- **Every attach answers for itself.** The agent run slice is reset when the
  view mounts, and a successful attach clears the standing refusal. Otherwise
  a denial outlives the socket that produced it: leaving the tab and coming
  back would show a live terminal beside a stale error, with steering greyed
  out even after `run.handoff` granted it.
- **Interactive revocation stays on the mirror socket.** The server
  re-checks a live attach's authorization every few seconds. Losing **Steer**
  sends an unsolicited control notification on the existing interactive
  WebSocket; the client applies its exact revoked generation, disables input,
  and remains a read-only mirror without replaying or reconnecting. A raw
  legacy (non-interactive) attach retains the named **1008** close, reason
  `steer permission withdrawn`. Membership withdrawal also uses **1008**,
  reason `membership withdrawn`, and stops reconnecting. A refusal frame's own
  close is handled only when no prior control response explains it.
- **Live Find, zoom, and clipboard share xterm's key handler.** `xterm-host.tsx`
  chains zoom, find, and `clipboardKeys` in that order; the first to claim a
  key stops it reaching the shell. `clipboardKeys` claims copy shortcuts but
  leaves native paste alive. `useTerminalImage` separately registers the
  capture-phase image listener described above, so an image event is claimed
  only when the current terminal has a live image handler and plain text never
  takes that path. `Ctrl+Shift+F` opens the find bar `TerminalPane`
  (`src/components/terminal-pane.tsx`) places above the terminal. Live search uses
  `@xterm/addon-search`; while reading, the same bar delegates to loaded archive
  pages and frozen rows. **No matches** comes from the active surface's answer
  rather than a tracked count. The read surface handles the same find, copy
  and zoom shortcuts without forwarding typing or paste to xterm.
  `Ctrl+=`, `Ctrl+-` and `Ctrl+0` move
  `UiSlice.terminalFontSize`, clamped to 8-32px by `clampTerminalFontSize` -
  on the way in from a keystroke and again in the store's `merge`, because a
  same-version reload never reaches `migrate` and xterm does not validate
  `fontSize`. The size is one persisted preference behind every terminal,
  applied to the live instance and re-fitted rather than by rebuilding it,
  which would throw the scrollback away. The static reading surface scales
  with that same preference while preserving its row-relative anchor.
- **DOM renderer, deliberately.** `@xterm/addon-webgl` 0.19.0 can reuse stale
  glyph-atlas positions under heavy glyph churn (xtermjs/xterm.js#6038), garbling
  scrolled rows until a forced refresh; the DOM renderer never desyncs. The
  terminals render in the shipped JetBrainsMono Nerd Font Mono
  (`src/lib/term-font.ts`, declared in `src/index.css`), so agent TUIs get
  their powerline and devicon glyphs at the same advance as text. The terminal
  opens only once regular and bold faces are loaded, because xterm caches glyph
  metrics synchronously at `open` and would otherwise bake fallback metrics in.
- **Delivered steers need no extra terminal work.** Once a Run Room steer
  request is delivered, the server writes the attributed member-coloured banner
  into the PTY stream itself, so it arrives as ANSI and xterm renders it like
  any other output.
- Board cards get no live terminal previews in v1 (spec cut-line).

The terminal's colours are the one place the tokens cannot be used directly:
xterm needs resolved theme values rather than the CSS variables, so the view
reads the computed background and foreground off its own host element and
re-reads them when the dark class on `<html>` changes.

## Shared run Browser

`src/routes/browser/` is the run-detail **Browser** tab in the same bundle
used by the local SSH gateway and server-hosted tailnet gateway. It observes
the run's actual isolated Chromium companion, not an iframe or a forwarded
preview host. App JavaScript, cookies, redirects, popups and hot updates run
there against the run's own network namespace: `http://localhost:3000` means
the app in the run, not the phone or laptop. No debugging/CDP endpoint is
exposed to the dashboard.

Opening the tab reads status, pages and ownership; it does not create a
session. Before a page is selected, the URL field and **Open browser** are the
primary path to launching explicitly. A selected page exposes **Go**, **Back**,
**Forward** and **Reload page** beside the URL. **Browser tools** contains the
secondary page, viewport, capture, reconnect and destructive controls.
**New page** opens another page in the existing context. The **Page** selector
includes popups and changes the selected page for the agent and other viewers,
so it requires control. **Viewport** offers desktop 1280 × 800, phone
390 × 844 and landscape phone 844 × 390; these change the real remote
viewport, not just the displayed image. They do not emulate a different user
agent, operating system or hardware.

The pane uses shared 13px inputs and buttons and native selectors styled with
`field`: 28px high for mouse input and 44px for coarse pointers. Primary
controls wrap responsively; secondary controls stay in Browser tools. Long
addresses, page titles and errors stay within the pane.

The header identifies the browser incarnation, lifecycle state and current
member or run-agent controller. **Acquire control** claims an unoccupied
browser; **Take over browser** explicitly displaces the displayed lease.
**Release control** gives up only that browser surface, not a durable mission
hold. Watchers see the same selected page but cannot navigate, resize, select
pages or send input. The server revalidates Steer, current membership and the
surface generation; a visible old control button never authorizes a stale
mutation.

Click or touch the image to interact. Keyboard shortcuts carry their
modifiers, pointer gestures include button/click count, wheel input scrolls
the remote page, and up to ten touch contacts retain distinct IDs. **Keyboard**
focuses the phone's text input bridge; committed composition/IME text is sent
once rather than forwarding intermediate composition candidates. Native
hardware-bound login flows and identity providers which reject automated
Chromium remain limitations; use test accounts rather than importing a
personal browser profile.

**Expand** fills the run pane with the selected page, hiding the run header
and navigation/capture controls. **Restore** brings those controls back.
Neither action reconnects the stream, changes the remote viewport, or
reacquires control. The current controller and errors remain visible, and
**Keyboard** remains available for phone input.

The stream accepts one bounded binary frame per WebSocket message (16 KiB
metadata and 2 MiB image maximum). It keeps one pending compressed image and
one decode, closes decoded bitmaps after painting, and never builds an image
history. Input uses the metadata of the frame actually painted, including
session/page revision and viewport ID. Coordinates exclude letterboxing and
undo image/page scaling. The ordered input buffer is capped at 32 operations
and one second; redundant moves for the same contact are coalesced. Changed
frame identity, expired input or changed authority discards pending input with
a visible error. Failed mutations are not automatically retried or replayed.

**Hide browser**, changing tabs, or closing the dashboard detaches observation
only. The app, pages and login continue according to the run's lifetime.
**Reconnect** reads surviving state and reconnects observation; it never opens
a fresh session. Reloading the dashboard creates a new tab-local control
identity and initially watches the surviving owner; taking over is explicit.
Stream failures, lifecycle unavailability and mutation refusals remain visible.
**Close page** and **Reset session** both require current control and use the
shared `AlertDialog` confirmation primitive. Close removes the selected page
for everyone while leaving the other pages and session; Reset destroys shared
pages/cookies and requires acquiring the new session before opening pages.
Each confirmation captures the session and control identity/generation, plus
the page and revision for Close. A replacement page, session or authority
invalidates it rather than retargeting the mutation. Cancel restores focus to
Browser tools, and raw failures remain readable in the confirmation. Closing
the run owns stopping the companion itself.

**Screenshot** calls the real capture API at its own recorded boundary, not
a canvas copy or the last received frame. It creates a private transient
capture and displays its ID. Open the existing **Evidence** drawer to inspect
and explicitly select captures/verification notes for retention. Taking a
screenshot does not publish or automatically retain anything.

`web/e2e/development-browser/` adds real-server Playwright scenarios using the
existing server/SSH-gateway harness, a Node 22.14.0 app process bound only to
run-loopback, and the real browser companion. They cover invalid credentials,
cookie-backed sign-in/logout, module hot replacement without logout,
desktop/phone input, native Chromium composition, multiple touch contacts,
same-page run-principal actions, popups, detach/reconnect, watch/takeover and
stale control/viewport rejection. The app container publishes no host port.
Use the normal E2E binary/build prerequisites and a built `aether/browser:test`
image, or the harness's inherited `AETHER_BROWSER_IMAGE` override. The
deterministic harness only holds the run alive: these are not proof of an
authenticated vendor model/tool loop, nor an authenticated Tailscale-hosted
device run.

## Run events tab

`src/routes/terminal/events.tsx` is the run-detail Events tab: the workspace
activity feed pinned to the run in view. It drives the same feed slice and
paging readers and live appends the team activity view uses (`openFeed`,
`useLiveFeed`, `olderFeed`), and both views render rows through the one shared component
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
  grammar - why neither Monaco nor CodeMirror is a dependency. The server
  sends complete run diffs up to 64 MiB, and the parser remains tolerant of
  incomplete input from a failed transport.
- **Long lines wrap or scroll, and the pointer picks which first.** Wrapping
  breaks the column alignment a diff is read by, and side-scrolling means
  panning every file section separately - which a phone cannot do well. So
  **Wrap lines** in the stats row is a toggle, starting on for a coarse
  pointer and off for a mouse. The choice itself is a view preference on the
  UI slice (`diffWrap`), stored like the sidebar width, because only one
  run-detail route is active at a time and component state would forget it
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
a view of its own in the shell: watchers reach the run card through its
slot, presence and spend are the **Team** line in the sidebar footer menu, and
the two full views are registry routes (`approvals`, `timeline`): Activity is
a sidebar row, Approvals is reached from the palette, and each is gated on the
method it needs.

The approval inbox is for agent permission and plan approvals. Run Room
questions and queued steer requests stay contextual to their run. Unanswered
questions to the owner put the run in **Needs you**; both contribute to the Run Room
count, and neither creates a second action inbox.

- **Events keep them current; a full read only fills gaps.**
  `useTeamRefresh` in `src/routes/team/sync.ts` reads everything once on
  mount, again whenever the event stream comes back live - a reconnect that
  re-hydrates does not replay what it missed - and when the workspace set or
  **Show decided** changes. Between those, `applyEvent` updates the store
  from the events that change these reads:
  - `workspace.approval` names the request and its decision but not its
    text. A request the inbox holds is decided in place (and dropped while
    decided requests are hidden); an unknown one reads that one workspace's
    `approval.list`. Each such event is counted per workspace, and a read
    that started before it - full or single - does not overwrite its
    workspace; that workspace is then read again on its own. A failed
    single read sets the inbox error and retries after 5 s; while the
    stream stays live it keeps retrying, doubling the wait up to 60 s,
    until a read succeeds or a full read clears the error.
  - `workspace.budget` carries the state, cap and spend, applied as they
    are. It fires on threshold crossings, refusals and admin edits, not on
    every spend change, so `run.cost` re-reads that workspace's
    `budget.get` to keep the spend and unmetered count current, once 1.5 s
    pass without another result for it. Each budget event is counted per
    workspace, and a read that started before one is dropped. A failed
    read retries with the inbox's backoff (5 s doubling to 60 s) until a
    read succeeds, a budget event lands, or the stream leaves `live`.
  - `workspace.presence` names one transition, so it re-reads
    `presence.roster`.
  These reads start without holding up the events queued behind them (only
  a run fetch does, for the cursor rule), and each is coalesced in
  `src/store/coalesce.ts`: per workspace, one in flight and one queued, so
  a burst of results or attaches costs two requests.
  `AppShell` mounts the hook once, which is also where the presence
  heartbeat lives
  (every 15 s, with the disk gauge read beside it). It also reads and beats
  on `onWake`, because a backgrounded tab freezes its timers and drops its
  socket: a phone returns with its presence already expired server-side (the
  TTL is 45s) and may have missed an approval. That read keeps a 2.5 s floor,
  so app switching cannot turn into a request per workspace each time; the
  heartbeat is one request and always goes.
- **One refresh covers every workspace, and there is only the one.** These
  reads are per workspace on the wire, and a workspace is a repo plus its
  team settings. A deployment has a handful of them and they outlive every
  run in them. So `refreshTeam` reads all of them each time rather than
  splitting into a bounded recurring pass and a wide occasional one. Both
  readouts it feeds ask a whole-deployment question anyway - the Team line
  claims the worst budget state anywhere, and Needs you claims every pending
  request - and a workspace does not stop being over its cap or holding an
  undecided request when its last run finishes, so no subset could answer
  either one. Failures leave the last good data in place.
- **An unreadable queue says so.** `refreshInbox` keeps the first
  per-workspace `approval.list` failure, and the inbox and the footer menu's
  Team line report it rather than "Nothing is waiting on a decision.",
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
  **Show decided / Hide decided** belongs in `ViewHeader.actions`, beside the
  inbox heading, not a second toolbar.
  Decisions go through `approval.decide` with the run the request belongs to,
  so the server attributes them and applies the steer check: a refusal is
  rendered as the server's answer, never predicted by the form. A request the
  user has just decided stays on screen reporting its outcome, laid over the
  fetched queue, because the next fetch no longer returns it.
- **The feed opens at the end of the log.** `workspace.timeline` pages forward
  from a cursor only, so the view first asks for a page past the end - that
  answer carries the log head - and opens a window back from it. After that
  the feed is live without reading: while a feed view is mounted
  (`useLiveFeed`), `applyEvent` appends each event the filters select by the
  server reader's own rule (workspace, run or mail to it, actor, type;
  `run.diff`, `run.title`, `run.agent` and `coord.message.acked` only when
  asked for by type), skipping a
  sequence already held. The cursor follows only while the window is whole -
  no read in flight, failed or cut short - and a view reads from the cursor
  again when the stream reconnects. A failed or cut-short read is retried on
  the next event applied (a failed opening read reopens the window), so one
  bad response does not pin the error until the user changes a filter. A
  read that reaches the log head clears the cut-short notice. "Load older"
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
- **A budget warns, it never stops anything.** The Team line in the sidebar
  footer menu shows the spend and the worst state any workspace is in (`ok`,
  `warn`, `exceeded`) - every workspace, ones with nothing running included,
  which is what the wide read above is for - and says so in those words. A spend that includes unmetered runs renders as a floor
  (`$1.20+`), because a harness with no adapter reports nothing.
- **Workspace controls live on the workspace view.** The workspace switcher's
  **Repository** item opens it.
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
- The full read and the heartbeat interval read `GET /api/v1/disk` and write
  it onto the stored `server.info`, which is what fills the disk gauge in
  Settings > Server.

## Devices and invitations

`src/routes/devices/` lists the computers members reach the server with
through an edge, with each device key's fingerprint: the member's own, or
every member's for an admin, and each device's status: `approved`,
`pending`, `registered` (admitted by signing in under `edge-access
account`) or `revoked`. It approves a pending or registered device only by
the code typed in from that device, which no row shows: **Review** looks the
code up with `member.device.lookup`, and a dialog shows the device, the
account it signed in as, and the member and role approving admits it as,
before **Approve** sends `member.device.approve` with that device's id. It
revokes a device too, and shows every server refusal verbatim. Device management
is its own capability-gated view rather than a machine-local Settings section:
both gateways can serve it, and the sidebar and palette show it whenever the
gateway serves `member.device.list`.

The Members view carries an admin-only **Invitations** section
(`src/routes/members/invitations.tsx`) for edge accounts: a GitHub login or
an email, a role, and revoke. Its button reads **Invite account**, so it is
not confused with **Invite**, which mints one-time codes for SSH-key joins.
The server records every new invitation for a GitHub account; an email
invitation an earlier version stored for another provider shows that
provider as stored, such as `dana@example.com on google`. The section says
what an invitation admits under each policy, because `server.info` does not
report the policy: under `account` the first connection makes the account a
member; under `approved-devices` its device waits until an admin approves
it with its code.

## Manage workspaces

`src/routes/workspaces/` lists workspaces and offers **Add a workspace** even
when others already exist. Open it from the workspace selector, shared
navigation, the command palette, or **Add another workspace** on a workspace
page. Each row shows its name, creation time, base branch and steering policy,
with **Open** and **Link local repository** actions.

The same creation choices appear in onboarding:

- **Public or private remote repository** opens **Import repository**.
  Public sources use credential-free HTTPS. Private sources use a server-held,
  read-only deploy key, installed by an administrator of the upstream
  repository. Generic SSH sources also require independently verified
  `known_hosts` entries. Import retains the new workspace even if fetching
  fails; continue to **Source control** on that workspace rather than creating
  it again. Verify the key, review the observed commit, and explicitly adopt
  its generation. Reconfiguring rotates the key; verifying does not.
- **Local clone** opens **Create from local clone** on a local gateway.
  Name the workspace and the base branch that exists in the clone, then link
  its absolute path and push that branch. A hosted gateway instead gives the
  desktop/CLI path; it neither creates an unusable local workspace nor pretends
  to browse the member's filesystem.

Creation and remote source administration require an administrator. A
collaborator can link a clone to an existing workspace; a viewer cannot push
its base. **Repository settings** on the workspace page, also reachable from
**Workspace settings**, reopens source setup and local linking without restarting
onboarding. It names the workspace ID, base branch and checkout Origin.

Admins also get **Delete**. The confirmation lists what is permanently
removed; **Cancel** leaves the workspace untouched. The server refuses active
work, pending cleanup and configured schedules rather than stopping them.
Failures remain in the dialog verbatim, so the admin can resolve the blocker
and retry. See [workspace deletion](teams.md#workspaces) for the CLI and
cleanup rules.

![Workspace deletion confirmation](media/workspace-delete-confirmation.webp)

![Deletion refused while a schedule remains](media/workspace-delete-refusal.webp)

After deletion, the list and workspace switcher update together.
`workspace.deleted` events reconcile other connected dashboards, including
their selection and any open deleted workspace or run.

## Settings

`src/routes/settings/` is available through every gateway. **Appearance**
provides explicit **System**, **Light** and **Dark** choices, backed by the
existing persisted theme preference and live system-scheme effect. The palette
and the sidebar footer menu offer the same choices.
**Single-key shortcuts** turns the character keys of the keybinding table on
or off (see [Keyboard and focus](#keyboard-and-focus)). **Server** names the
server version and protocol, shows the disk gauge with what is filling it
(worktrees, transcripts, database, repos, free space), and holds **Usage**,
the subscription usage reader.

Machine settings keep their exact local capability gates. The machine section
requires `daemon.status` or `sync.status`; within it, the local link requires
`link.status`, the daemon requires `daemon.status`, and **Mirror run files to
your repository** requires `sync.status`. The latter starts and stops a live
run's sync overlay. The server-hosted dashboard omits these machine-local
controls, not Appearance.
The local link section points to **Manage repositories and workspaces**.
Workspace repository settings, base freshness and Source control live on the
workspace page on either gateway. A configured mirror is refreshed there with
**Verify/Refresh**; a local-only workspace uses its pushed base. There is no
recurring base-refresh button.

## Onboarding wizard

`src/routes/onboarding/` is the guided first-run path. A local gateway offers
Link, Git identity, Workspace, Repository, Agents and First run. A hosted
gateway starts at Git identity: remote import, server-side agent setup and
configuration do not require a clone or SSH identity on the browser's machine.
Only the local clone controls require local capabilities, and their hosted
replacement names the exact desktop/CLI handoff.

Link, Workspace and First run live in `steps.tsx`. The shared
`workspace-create.tsx` and `workspace-repository.tsx` components keep initial
and subsequent workspace setup on the same paths. Local linking is
`repo-step.tsx`, Git identity is `git-identity-step.tsx`, and Agents is
`agents-step.tsx`, using `github-connect.tsx` and `profile-import.tsx`.

Navigation is two levels: the step index, and one sub-screen name owned by
whichever step has sub-screens. The Agents step owns both of today's - a
harness's setup screen, named by the harness, and the GitHub connect screen,
named `@github` - and the wizard holds the name, so **Back** closes an open
sub-screen first and leaves the step only from the step's own screen. A step
with sub-screens takes them as `setup` and `onSetup` rather than keeping them
in its own state.

The wizard passes a **Back** action to each step. Repository setup also has
**Back to repository choices** while a local clone is open. Returning from
Agents preserves the selected workspace and repository state; changing
workspaces never applies the previous workspace's link or Git result.

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

The Repository step offers remote source setup before any local clone is
linked. **Link local repository** opens the local path form; its path must be
absolute, accepting a leading `/`, a drive letter or a UNC prefix. The field
offers folders `link.status` already knows: the current clone and named
profiles as a `datalist`. In the desktop shell it also gets a **Choose
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
server and rejects direct base writes; run branches can still be pulled.
The dashboard withholds base push controls while an administrator's source
status is unresolved or identifies a mirror. Configuring or fetching a source
never silently authorizes its candidate.

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

Link, push and fast-forward always carry the selected workspace ID. Each
server profile has one current clone, not a clone per workspace. The UI
rechecks `link.status` on entry, focus and before Git mutations; a remembered
connection for a different current clone must be linked again. The gateway
also checks that the clone's `aether` remote names the requested workspace.

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

The UI slice persists the resume point, furthest step reached, selected
workspace, source choice, connected repository and First run draft. Finishing
the wizard or navigating away clears these fields. Steps are stored by name
rather than position, so inserting a step never
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

Hydration reads `link.status` first for a local gateway. A linked machine with
no in-progress workspace is marked onboarded on initial hydration. An
unlinked local gateway routes here when `onboarded` is false. Hosted onboarding
also resumes after reload when its saved workspace still exists, even after
the first import. A linked clone must not erase a resumed wizard's workspace
or draft, and a reconnect must not redirect navigation.
Authorized run deep links take precedence. Completing the final step or
navigating elsewhere marks the UI onboarded and clears wizard state.

The Link step first offers signing in to an edge
(`src/routes/onboarding/edge-link.tsx`): it runs the edge's device flow
through the local gateway, which keeps the device token, shows the code
while it polls `edge.status`, then links a server or claims a new one
with the code `aether-server setup` printed. Signed in to more than one
edge, it lists them and shows nothing to link until one is chosen; the
server list, link by id and claim then pass that edge to the gateway, as
`--edge` does on the command line. **Server id from your admin**
links by an id typed in, which the edge cannot substitute. A server picked
from the account's list opens a **Confirm server** panel with its id and
the host key fingerprint `edge.hostkey` read, and links only on **Link and
pin**, because that id comes from the edge. **Link by
address** swaps the sign-in for the address form, for a tailnet or SSH
server, and **Sign in instead** swaps it back; showing one at a time keeps
the address field above a phone's soft keyboard. The Link step
distinguishes no configured server, a server with no repository,
and a fully linked server. It refreshes on Retry and when the window regains
focus, so a separate `aether link` command appears without restarting the GUI.

The Agents step has three optional parts and never blocks: **Skip for now**
is reachable from every state, including an open setup shell and a failed
configuration import.

Part A lists the server's `agent.list` inventory on either gateway. An
installed executable is reported as installed in the member's server
environment, not on the browser's computer and not as proof of vendor login.
**Set up** embeds the same `AgentWizard` as the Agents page; the harness is
already selected, so it opens the `agent-setup` shell without another form.
An empty inventory offers the custom-agent form. Setup confirmation checks
`agent.list` for an installed
executable, then runs `env.save` - the call the dock's **Save environment**
button makes - before handing that agent to the First run step, which
preselects it. The save is the point: an executable that exists only in the
running container is not in the image runs start from. The done screen names
the saved image. A missing executable, a failed check or a failed save keeps
setup open for retry with the real error. The vendor login is not checkable
from here, and the copy says so.
Reopening setup for an existing member-defined agent does not register it
again or replace its launch arguments, profile root or credential policy.
Custom agents without an installer command use their vendor's installation
instructions; the dashboard does not type a fabricated command.

The permanent **Agents** page also exposes **Set up / log in** per agent,
**Git commit identity**, **Connect GitHub** and configuration import, so
returning members do not have to reopen the wizard. Git identity records
commit attribution; vendor login and native Git publishing credentials are
separate. GitHub controls use server RPC and work on hosted gateways that
advertise those methods.

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
moved without them. Every remedy the copy names is reachable from the dock
right below: **Save environment**, or **More** for **Reset to standard** and
**Stop environment**. **Check again** re-runs the probe, because none of those
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

**Configuration import** in this step renders the same
`src/components/profile-import.tsx` component as the permanent
[Configuration view](#configuration-view). It is optional here and remains
available from Agents, shared navigation, and the palette on either gateway,
independently of onboarding or workspaces. `config.roots` supplies destinations
such as `~/.claude` and their runtime exclusions. A unique basename selects
the destination automatically; an unknown or ambiguous basename requires a
choice before file bytes are read. Retained browser `File` handles allow the
metadata preview to be recomputed after a destination change.

Credential names in any path component and `*.pem` files are excluded before
read; destination-specific `runtime_ignores` match exact root-relative paths or
component prefixes case-sensitively. Metadata previews, bounded complete
transfers, progress, repeat-import controls, and server scanning are identical
to the permanent route. A response with `error`
shows committed counts, exact canonical `imported_paths`, and the real error,
and warns that copied files remain. A lost RPC response leaves the outcome
unknown; inspect **Files** before explicitly importing again. Auth/vendor login
is separate.

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

Before enabling **Launch**, the step checks the selected workspace's base
through `files.tree` where available and reads available mirror status for
every role. A mirrored workspace must have a ready source and an accepted
commit. Missing branches, pending authorization and source errors remain
visible with **Review repository setup** and **Check source again**. Empty
workspace creation is not source readiness. The server rechecks permission
and source policy at launch.

The step's "No agent subscription yet?" note points at the CLI and stays on
screen once a workspace is chosen; "Prove the plumbing without an agent subscription" in
[quickstart.md](quickstart.md) covers what `fake` is and how to launch it.

## Update prompts

`src/components/update-banner.tsx` is where the dashboard says a binary is out
of date. `UpdateCenter`, mounted once by `AppShell`, runs the reads below and
hosts the **Updates** dialog, which lists every prompt that applies, with the
CLI one in `src/components/cli-update-banner.tsx` and the pieces they share in
`src/components/update-banner-shared.tsx`. The sidebar shows one notice row
from `useUpdateNotice()` - "Aether 0.5.3 is available · Update", "Server 0.5.3
is available", "The desktop app is out of date" or "Server update in
progress, terminals reconnect briefly" - and its **Update** opens the dialog. The CLI and shell prompts read
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
  what the notice row reads. A re-check that fails is swallowed, as the
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
  *Server update in progress, terminals reconnect briefly* in the sidebar's
  notice row while one is in flight, so a restart nobody explained
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
- **The footer menu brings a dismissed prompt back.** **Update…** in the
  sidebar footer menu appears while any update exists, dismissed or not; it
  clears the dismissals and opens the dialog.
- **The desktop shell has a banner of its own.** The SPA ships inside the CLI,
  but the Electron shell around it is whatever `aether gui build` last
  produced. That build records the complete CLI version and its executable
  path alongside the shell's npm-valid `package.json` version. `desktop/main.js`
  starts the recorded binary ahead of `PATH` (unless `AETHER_BIN` explicitly
  overrides it) and hands the build version to the renderer; `desktop/preload.js`
  exposes it as `window.aetherDesktop.shellVersion`. On a local gateway, a
  different capabilities version raises the app-out-of-date banner with
  `aether gui build`. A browser tab and a server-hosted dashboard do not
  have a local shell to compare. The banner is independent of
  `update_available`: the CLI is usually current *after* an update that
  left the shell old.

## Styleguide

- **Tokens only.** See [styles.md](styles.md) for the palette, interaction
  tokens, `--state-*` tokens, geometry, radii, fonts and the one inline-colour
  exception for member attributes. Components use semantic token classes rather
  than route-specific colour literals.
- **Dark, light, system.** Settings > Appearance and explicit palette commands
  set the stored preference; `system` follows `prefers-color-scheme` live.
  There is no additional theme mode or cycling status icon.
- **Typography and density.** Inter at 13px with 12px supporting copy, on
  the type scale in [styles.md](styles.md#type). JetBrainsMono NFM is the
  terminal's alone; code uses the system monospace stack; VT323 remains only
  the Aether wordmark. Use the 28px control and row, 44px pane header and
  24px small-control geometry, and avoid promotional titles or oversized
  cards.
- **Flat shell and palette host.** One 220-400px sidebar beside the content
  view; under 768px a 48px top bar and the sidebar as a side sheet. Mount the
  command palette, the shortcuts dialog and the updates dialog once in
  `AppShell`.
- **Harness and state are separate.** The harness glyph says who is running;
  the state indicator or badge says what state. Board cards use a labeled
  state badge without a duplicate dot. The five presentation states are
  described under [Run state](#run-state).
- **A working run moves where an indicator is used.** `StateIndicator` swaps
  the static dot for three dots bouncing in `--state-working` in run headers
  and run lists. Sidebar rows keep one dot and pulse its opacity; palette rows
  stay static. The fixed dot box prevents a row shifting when a run starts or
  stops.
- **Motion is optional.** The controlling terminal's 1px teal outline is always
  static. Working dots and the sidebar pulse stop moving under
  `prefers-reduced-motion: reduce`. The original
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
  Shared `Label` captions are block-level; stacked caption-to-field gaps must
  measure 4px, including wrapped inputs, rather than relying on inline margins.
- **The empty string belongs to the Select placeholder.** Use a named,
  non-empty sentinel for an empty API or filter value, then map it back at
  that boundary. The Select wrapper ignores the empty report Radix can send
  through its hidden native select while options arrive.
- **Name the Select trigger.** Pair the caption's `htmlFor` with the trigger's
  `id`, so the control announces its purpose rather than only its current
  option.
- **Disclosures normally unmount closed content.** `CollapsibleTrigger`
  supplies the marker.
- **Status and metadata pills are `Badge`.** It takes a run-state `tone` or
  stays neutral. A `Badge` takes no `title` of its own, so a pill whose text
  can be clipped wears one on the span around it. Run and dock tab strips
  retain their custom manual tab semantics.
- **A hint a reader needs is a `Tooltip`, not a `title`.** A `title` is drawn
  by the pointer and by nothing else, so on a control a keyboard can land on
  it is information that reader can never get at. A Tooltip opens on focus and
  hover, with a 300ms hover delay, and points the control's
  `aria-describedby` at itself. It is a description rather than the name: an
  icon-only `Button` takes `label`, which sets both. A `title` stays only on what a
  keyboard cannot land on - a truncated path, a timestamp, a breakdown -
  since a tooltip needs a focusable trigger, and a hint on a mark would buy a
  tab stop per feed row or per card. A mark that names itself
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

Run actions cover the retained-run contract: Close chooses merged or abandoned,
while Relaunch appears only for eligible retained TUI runs. Release is also
offered for finished retained mission workers; its confirmation and error path
do not hide or delete history. Expired or unavailable runs offer no Release.
Sidebar tests cover group disclosure, with Finished collapsed by default.
`src/lib/needs-you.test.ts` has one case per Needs you condition.

`src/a11y.test.tsx` exercises the run tab strip, dock tabs and sidebar splitter
with keyboard events: arrow navigation, Enter and Space activation, Delete and
Backspace close, focus handoff, clamped resizing and collapse. `src/components/shell/nav-shortcuts.test.tsx`
covers the shell shortcut precedence across fields, dialogs, menus, lists and
selects, and the Single-key shortcuts setting; `src/lib/keybindings.test.ts`
fails on two bindings sharing keys in overlapping scopes.
`src/components/ui/fields.test.tsx` checks that a wrapped Label names
its actual input and that an Input ref reaches the field DOM node. These are
behavior assertions against rendered controls; source scans, literal class
assertions and CSS text checks are not behavior coverage.

Route and shared-header tests cover the board, global overview, terminal,
Browser, diff, files, workspace, onboarding, team, members, settings, agents,
templates, palette and update surfaces. Keep assertions on what a member can
observe: a route or control
appears or disappears under its capability and role, a focus or navigation
action lands in the expected view, a loading or error message is shown, or the
API receives the mutation only after the relevant confirmation. Preserve
verbatim gateway errors in assertions when they are part of the contract.

`web/e2e/` is the layout and browser-behavior layer. Playwright drives a real
browser against the local `aether gui` gateway and the server it proxies, while
the same static bundle is also usable through the server-hosted gateway.
`keyboard-focus` checks Escape ordering across an open dialog, the footer menu
and a run view and confirms that a focused control paints the app outline.
`window-sizing` exercises the updates dialog at the smallest desktop window
and below it. `run-deep-link` checks that the address follows navigation,
survives a reload and agrees with back and Escape.

The touch shell is driven by the `mobile` project, which
[testing.md](testing.md) describes: `shell-drawer.mobile.spec.ts` opens the
phone drawer, taps a run and finds the drawer gone with the run on screen and
focus on its heading;
`dialog-anchor.mobile.spec.ts` checks that a dialog short enough to tell the
two apart sits at the top rather than the middle, and that the launch form
keeps its footer on screen on a viewport as short as a keyboard leaves;
and `toast-clearance.mobile.spec.ts` checks that a toast comes to rest 8px
clear of the bottom edge. `run-views.mobile.spec.ts` steers a
real run from the header's labeled actions and More menu, then reads its diff.
`sidebar-drawer.spec.ts` stays on the desktop project because its keyboard
contract - `Mod+B` closing the drawer and the palette coming back once it is gone - needs
a narrow window with a keyboard rather than a phone. `board-card`, `run-switch`,
`run-attach-retry`,
`run-provisioning`, `terminal-tools` and the onboarding scenarios cover the
corresponding real UI transitions, gateway responses and terminal behavior.
Run the full browser workflow with `make test-e2e`; its scenario inventory and
setup details live in [testing.md](testing.md).

Room scenarios (`run-room.spec.ts` and `run-room.mobile.spec.ts`) cover shared
comments, moderation, explicit control transfer and the phone sheet.
`run-evidence.spec.ts`, `run-evidence.mobile.spec.ts` and the Room phone
scenario cover retained evidence and bounded, tappable phone presentation.
`src/routes/terminal/run-dock.test.tsx` covers Evidence access from an empty,
collapsed dock without starting a process; `run-room.test.tsx` covers its
comment-draft handoff, and `terminal.test.tsx` covers occupied-lease
confirmation fencing. `src/routes/browser/index.test.tsx` covers progressive page
controls, close/reset identity fencing and raw refusals.
`src/routes/settings/settings.test.tsx` covers universal Appearance without local
RPCs.

Use the browser workflow for real computed geometry, top bar and sidebar
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
same tailnet. Expect hosted onboarding for a member who has not completed
setup, even when other members have created workspaces; completed members
start on the board. WhoIs already identifies the member. The machine-local
Link step, update notice, pull, forward and sync controls are absent
because the descriptor carries no `local` verbs. Repository linking instead
shows a local-client/CLI handoff. Settings remains available for Appearance.
Files and configuration editing also remain available through the
server-hosted gateway.

The installed app is a manual check too, because no browser lets a test
emulate the `display-mode: standalone` a real install gives. On Android,
Chrome's ⋮ menu should offer **Install app**; on iPhone, Safari's **Share >
Add to Home Screen**. The icon that lands on the home screen should be the
Aether mark on a dark tile, and opening it should give a full-screen dashboard
with the shell's own top bar, safe-area padding intact under
the notch, and the soft keyboard still shortening the layout rather than
covering it. Watch what the server saw with:

```sh
journalctl -u aether-server -f
```

Prerequisites and the refusals a bad tailnet setup produces are in
[networking.md](networking.md#the-dashboard).

The [Android app](install.md#android-app) is the same page in a WebView, so
the same expectations hold there. It is worth checking separately for two
things the browser does not exercise: whether the top bar and the content
clear the system bars, which depends on the WebView forwarding safe-area
insets, and whether the soft keyboard shortens the layout rather than covering
it.

```sh
make android-debug
adb install -r dist/aether-android-debug.apk
```

A debug APK allows `chrome://inspect` from a computer on the same USB
connection, which is how to read the page's console and its computed
`env(safe-area-inset-*)` values on the device. A release APK does not. The
shell falls back to padding for the system bars itself on a WebView older
than Chromium 140, so check `chrome://version` on the phone before
concluding the page is wrong.

## Missions and swarm creation

The launch dialog keeps **Single agent** as its default. When the gateway
advertises `mission.create`, it also offers **Swarm**: one concise objective,
an integrator account and harness, and an explicit list of allowed
account/harness/mode execution choices. The integrator always runs in `tui` mode, because
`mission.create` and `mission.replace-integrator` refuse a headless
integrator, so the swarm form has no integrator mode field; worker rows keep
their own mode. `mission.create` refuses an integrator whose exact
account/harness/mode is not one of `execution_choices`, so the list always
starts with a checked, disabled **Integrator** row that follows the
integrator fields and reads `tui`. A ticked worker row with the same tuple
is not sent twice, and the list is sent sorted by account, harness, and
mode, so the same set is always the same request. The worker rows default to the
integrator's account and first installed harness in `headless` mode. Its
submit button is **Create swarm**, matching the missions header action, and
success toasts `Swarm created`; creating a swarm starts the integrator, not
the workers. The form sends the exact selected values to `mission.create`,
including a client idempotency key, then navigates to
`missions/<server-issued-id>`.

Swarm is offered only while `cap.hasMethod('mission.create')` and the
member's role may launch. The dialog opens on Swarm from the Missions route
or the palette's **Create swarm...** entry (`openPaletteDialog('swarm')`,
listed under the same two conditions) when both hold, and on Single agent
otherwise. If either stops holding while the dialog is open on Swarm - a
re-hydration that could not read the capabilities, or a role change - the
dialog stays on Swarm with **Create swarm** disabled and says why, naming
missing capabilities before the role:

- `Swarm launch is unavailable: the server did not report its capabilities. Switch to Single agent to launch a run.`
- `Swarm launch is unavailable: your role cannot launch.`
- `Swarm launch is unavailable: the gateway does not offer mission.create. Switch to Single agent to launch a run.`

The **Launch type** select stays so the member can switch to Single agent.

The key belongs to the submitted contents, not to the dialog: the tab keeps
one key per distinct set of contents in memory until a create with them
succeeds. A failed create may already have stored the mission, so resending
the same contents - after edits and back, or after closing and reopening the
dialog - sends the same key and the server replays that mission. Changed
contents get their own key, because the server refuses changed contents
under a used key as `store: mission idempotency conflict`. After a success
the same contents start a new swarm. Client-generated IDs are never used as
mission authority.

`routes/missions` is registered through `routes/index.ts`, and the
`MissionsSlice` is composed into the root store. Hydration reads
`mission.list` for the active workspace; mission events refetch either the
open `mission.show` projection or the first list page, so reloads and event
reconnects recover server state rather than retaining a demo snapshot. The
refetched page is merged into the list, so older pages loaded with **Load
older missions** stay, along with the cursor for the next one. The slice
records which workspace the list and cursor were read for
(`missionListWorkspace`): a cursor read for another workspace is replaced by
the fetched one, and **Load older missions** only follows a cursor read for
the active workspace. The
progress view renders the authoritative task statuses Ready, Working, Review,
Done, Proposed and Abandoned. It keeps blockers, exact task revision/scope,
attempt IDs, evidence availability, and accepted submission
revision/artifact references visible. A worker success or exit is not enough
to render Done: the server must accept a submission for the current task
revision, with the required evidence available and any scope disposition
explicitly recorded.

### Mission phases

A mission is in one of four phases - `planning`, `active`, `completed`,
`cancelled`. Only `active` dispatches workers. No phase waits on a human
except for the answers to questions the integrator chose to ask. The phase
chip replaces the generic `Mission` chip on every mission card: `Planning`,
`Planning · N questions for you`, `Active`, `Completed`, `Cancelled`. The
detail view's status line answers the phase first and falls back to the
task-derived string only in `active`.

The detail header shows the objective's first line, cut to 80 characters
with an ellipsis, and carries the full objective in its `title` attribute.
The full objective opens the scrollable detail, clamped to three lines with
a **Show more** toggle when it overflows. Mission cards clamp the objective
the same way.

A phase banner sits under the objective in every phase and says what is
happening. When `current_integrator_run_id` names a run whose status in the
store is terminal, the banner adds `The integrator run <id> has exited;
replace the integrator to continue` with the **Replace integrator** control
inline, in `planning` and `active` - an integrator that exited is recovered,
not hidden behind a friendlier message. A `completed` or `cancelled`
mission's integrator stops on purpose and `mission.replace-integrator` is
refused there, so the sentence and the control are not shown, and
**Replace integrator** leaves the authorization section too.

When `current_integrator_run_id` names a run the hydrated store does not
hold, the detail view asks `run.get` once for that run ID; a create's
response can reach the dashboard before the run's first `run.status` event.
A returned run is added to the store. While the request is open, and before
hydration, the banner keeps its phase copy. Only a not-found answer means
the server holds no run for it: in `planning` and `active`, the banner then
replaces the phase sentence with `The integrator run has not started.`,
says the server retries the launch periodically and logs `mission: recover
integrator`, and offers **Replace integrator**. When the mission's
`integrator_run_launched` is true the run existed and is gone, so the
banner says `The integrator run was deleted; replace the integrator or
cancel the swarm.` instead. Any other `run.get` failure shows its error
above the mission and keeps the phase copy; **Refresh** asks again, once.
**Open integrator run** appears only once the store holds the run, beside
the run's status chip - the same state vocabulary as the run list - and its
last status reason.

While the mission carries `integrator_launch_error`, the server's reason the
integrator run last failed to launch, both the not-started and the exited
banner add `Last launch failure <time ago>: <error>`, and the mission card
in the list adds `Integrator did not launch: <error>` in `planning` and
`active`. The server clears the field once an integrator run launches.

**Replace integrator** offers each distinct account and harness in the
mission's `execution_choices`, of any mode, and always sends mode `tui`:
the server accepts a replacement only from those choices and runs it
interactive.

In `planning`, **Questions from the integrator** lists every question the
integrator asked. Questions are optional - the integrator asks only when the
objective is ambiguous - so the section can stay at `The integrator has not
asked anything yet.` for a whole mission. Each unanswered question takes a
textarea and an **Answer** button sending `mission.question.answer` with the
deterministic key `question-answer-<question_id>`, so a retry replays rather
than answering twice. Answered questions show the answer and who answered.
If a question this member is typing into arrives answered - the
`mission.changed` refetch replaces the whole projection - the textarea stays
mounted with the draft intact under `Answered by <display name>`, rather
than dropping what was typed. The tasks the integrator has proposed render
read-only under **Proposed tasks**. The answer form needs the capability,
launch permission, and the mission's accountable human or an admin:
`cap.hasMethod('mission.question.answer') && allowed('launch', self) && (self.id === mission.accountable_human_id || self.role === 'admin')`.

In `planning` and `active` the header also offers **Cancel swarm** to the
same identity, gated on `mission.cancel`. It opens a confirmation;
confirming sends `mission.cancel` with a key minted when the confirmation
opened, so a retry after a failure replays rather than cancelling twice. The
mission moves to `cancelled` and its workers and integrator run are stopped.
A refusal - the mission completed while the dialog was open, for example -
shows the server's error inside the dialog.

Outside `planning` the tasks render under **Tasks**, followed by **Candidate
progress** - the read-only candidate panel described in [Candidate review in
Run evidence](#candidate-review-in-run-evidence), listing only this mission's
candidates (`integration.list` with `mission_id`) and polling every five
seconds, because integration changes publish no event - and the questions, if
any, collapsed into **Planning questions**. A task carrying a `pending_revision`
shows a `Revision pending` chip and names the pending revision number and
title under the task. A pending revision is never the task's current
revision, so it cannot be dispatched until the integrator accepts it.

Attempt chips render wherever a task has attempts, so a completed or
cancelled mission still shows how each attempt ended. The `proposal`
blocker chip is hidden in `planning`: every task there waits for the
integrator to start the swarm, which the phase banner already says, and
repeating it as a blocker reads as a fault.

Answer and cancel failures live in component state and render through the
same `ErrorNotice` as a failed release, verbatim. They never go through
`setMissionError`, which the next `setMissionDetail` or `mission.changed`
refetch would wipe, and a failed answer leaves the draft intact.

Run links use the existing terminal route. Take control and Release control
continue to enforce the normal run controller and durable worker hold.
`mission.worker.release` is the human-only release action for a worker hold:
the dashboard sends the run ID and observed
`expected_takeover_generation` as a compare-and-swap, while the server
rechecks current steering authority under the same admission boundary. A
stale generation, revoked authority, or foreign control holder leaves the
hold in place and surfaces the conflict; an integrator or worker cannot
release it through the assignment-scoped socket. Integrator replacement is
gated by capabilities and role, pins `expected_generation`, and reuses one
idempotency key across retries while reloading the resulting mission. The
mission marker joins ordinary run cards through the `card:badges` slot; the
shell and board remain unchanged.

`mission.show` also carries bounded server-derived scope diagnostics. The
mission task view labels intended overlap, observed overlap and out-of-scope
paths and links each diagnostic to the task or peer run. The same diagnostics
are registered into the existing run-card conflict-chip slot, so overlap
warnings stay in the conflict experience rather than creating a second board
or lock surface.
