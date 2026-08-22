# Dashboard SPA (`web/`)

The browser client the server embeds and serves. Bun installs packages and
runs the scripts, Vite bundles, React 19 + TypeScript render, Tailwind v4 and
shadcn/ui (new-york, neutral, CSS variables) style, Zustand holds the state.

The gateway it talks to is documented in `docs/dashboard-api.md`; this guide
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
elsewhere with `AETHER_DASHBOARD=http://127.0.0.1:<dashboard-port>`.

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
one file each (`server`, `sessions`, `runs`, `members`, `terminal`, `board`,
`palette`, `approvals`, `presence`, `cost`, `timeline`, `diff`, `ui`). A new
feature adds a slice file and one spread in `createRootStore`.
Slices are typed against the whole root state, so a slice may read another's
data. Only view preferences (theme, sidebar width and collapse state, grouping,
the Idle column) are persisted; server data is always re-fetched.

Derived data (the sidebar tree, the attention-ordered run list) lives in
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

## Data flow

`connect()` in `src/store/sync.ts` owns the whole lifecycle. One round of HTTP
fetches hydrates the store (`server.info`, `session.list`, `member.list`,
`run.list`, `run.overlaps`, and `GET /api/v1/capabilities`), then `/ws/events`
is the only thing that changes it. The capabilities fetch may fail without
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
- **A `1008` close naming the dead token stops the stream for good.** The
  gateway's close reason distinguishes the token watch from a refused
  subscribe or a transient membership check, which the next reconnect can
  outlive and so are retried; only `dashboard token revoked or expired` is
  terminal, and the panes then say to mint a fresh token with `aether dash`.
- A `run.status` event for a run the client has never seen fetches that run
  before the event is applied, which is what keeps two quick transitions of a
  brand new run in order. If the fetch fails the event is unresolved: the
  cursor stays put and a fresh snapshot is taken to repair the store. An event
  naming an unknown session re-fetches `session.list`, because sessions arrive
  only by fetch and a run under an unknown session would render nowhere. An
  event whose actor is not in the members map re-fetches `member.list` for the
  same reason: no `member.*` event exists, so a teammate who joined after
  hydration would otherwise render as a raw ID forever. A
  `session.timeline` entry of kind `handoff` re-reads its run the same way,
  because a handoff publishes no `run.status` event to carry the new owner.

**The capabilities descriptor is the transport seam.** The store holds the
`GET /api/v1/capabilities` answer (`gateway`, `methods`, `ws`, `local`), and
`useCapability()` in `src/store/hooks.ts` wraps it as three predicates -
`hasMethod`, `hasLocal`, `hasWS` - with `methods: ["*"]` meaning everything.
When the descriptor is `null` (a legacy gateway), the fallback assumes the
remote surface: every method, `events` and `attach` sockets, no local verbs.
Views gate on these predicates rather than sniffing the URL, which is what
lets the same SPA serve the remote dashboard and the local gateway's
capability-gated surfaces (`docs/local-gateway.md`).

Every request goes through `src/lib/api.ts` - the only module that knows route
shapes, the bearer token, and error decoding. It carries exactly the methods
the views call; the team-feature methods arrive with the tickets that use them.
Every call is a `POST /api/v1/<method>` bar two `GET`s - the diff tab's patch
text and the status bar's disk number - because those are reads of a working
tree and a filesystem rather than RPC methods.
The token arrives as `?token=`
(from `aether dash --url`), moves into session storage, and is sent as
`Authorization: Bearer` on HTTP and as `?token=` on WebSockets.

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

`src/routes/board/` is the default center view: run cards in the four buckets
the GUI spec copies from Orca. `needs-attention` is Needs You (both stalls and
clean exits waiting on `run.close` - the card's reason line tells them apart),
`queued`/`provisioning`/`running` is Working, the terminal statuses are Done.
An active run whose approval request is still pending also presents as
needs-attention on the board and in the sidebar - the pause is invisible in
the domain status, so `runState` takes a pending flag fed from the approval
inbox. Cards sort by last state change, newest first; the columns are
untinted and the card carries the state colour.

Three things the buckets do not come from the run status alone:

- **Idle** holds *sessions* with nothing active rather than runs, because no
  run status maps to idle. It is hidden until the header toggle asks for it,
  and the preference is persisted.
- **Paused** is a badge, not a bucket. A paused run still reads `running` in
  the domain enum, so the wire `Run` carries a `paused` field the gateway
  decorates from the scheduler on `run.get` and `run.list`, never derived
  from the stored run. The hydration snapshot seeds the board's map from it
  (`seedPaused`, skipping runs without the field - a legacy gateway), and
  live `pause`/`resume` entries on the `session.timeline` event stream keep
  it current (`pausedFromTimeline` in `src/store/board.ts`).
- **Unseen** marks a run whose state changed since someone acknowledged it.
  An ack records the status *and* the change time, because `stateChangedAt` is
  recomputed from the run's timestamps on every fetch and can move backwards -
  a needs-attention run has no `finished_at`, so a re-hydration falls back to
  `started_at`, and a time-only comparison would mute exactly the card that
  needed the human. Acks live in the board slice and are app-wide: `navigate()`
  acknowledges whenever the route it is given carries a `runId`, so every
  surface that reveals a run mutes its sidebar row and its board card together,
  and the board header marks everything at once. They are session-scoped on
  purpose - nothing is acknowledged when the page loads, so a fresh tab shows
  what is waiting rather than remembering that yesterday's you looked at it.

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
`session.timeline` pause or resume arrives, and the palette offers
neither verb rather than the one the server would refuse.

## Command palette

`src/components/palette/` is the cmdk palette: `⌘K`/`Ctrl+K` anywhere, or the
button it registers into the `statusbar` slot (it has no home of its own in the
shell, and the dialog portals out of the status bar anyway). It jumps to runs
and sessions, carries the board's own commands, and steers **the run the center
view is showing** - any run-detail tab, since it keys on `route.params.runId`
rather than on a route name. From the board there is none, so reveal a run
first. The
two verbs that need prose, launch and inject, open a dialog; the rest call the
gateway directly and let the event stream report the result, so no palette
action writes run state into the store. A third dialog launches from a
template: it lists the chosen session's templates over `template.list` and
starts the run with `template.launch` (both on `lib/api.ts` like every other
call), then reveals it. Its open state lives with the dialog host in
`index.tsx`, not in the store's dialog union.

The launch form lists the harness names from `internal/harness` because there
is no `harness.list` method on the gateway's allowlist. A name the server does
not know is refused by the server, not by the form.

## Terminal view

`src/routes/terminal/` is the run-detail Terminal tab: xterm.js over
`/ws/attach/<run>` (`docs/dashboard-api.md`). The run-detail routes share one
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
- **A 1008 close is read, not guessed at.** The gateway closes a live attach
  with 1008 when its authorization watch fires, and the close reason names
  which gate fell: `steer permission withdrawn` just downgrades - the client
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
  desyncs. The terminal font stack ends in the shipped symbols-only Nerd Font
  (`src/lib/term-font.ts`, declared in `src/index.css`), so agent TUIs get
  their powerline and devicon glyphs; the terminal opens only once that font
  is loaded, because xterm caches glyph metrics synchronously at `open` and
  would otherwise bake fallback metrics in. The workspace-shell pane
  (`routes/shell/pane.tsx`) uses the identical renderer and font setup.
- **Injections need no client work.**  writes the attributed
  member-coloured banner into the PTY stream itself, so it arrives as ANSI and
  xterm renders it like any other output.
- Board cards get no live terminal previews in v1 (spec cut-line).

The terminal's colours are the one place the tokens cannot be used directly:
xterm needs resolved theme values rather than the CSS variables, so the view
reads the computed background and foreground off its own host element and
re-reads them when the dark class on `<html>` changes.

## Run events tab

`src/routes/terminal/events.tsx` is the run-detail Events tab: the session
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
  tab re-fetches `GET /api/v1/run/<id>/patch` (`docs/dashboard-api.md`)
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

`src/routes/team/` is presence, the shared approval inbox, the session
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
- **The recurring read is bounded; the wide read is not recurring.** Every one
  of these reads is per session on the wire, and `session.list` returns every
  session that ever existed - nothing retires them - so re-reading all of them
  on every burst of events would grow with the age of the deployment rather
  than with what is happening on it. `refreshSessions` bounds *that* path to
  the sessions with a run still going, whatever the centre view is showing,
  and any session whose stored inbox still holds an undecided request - a
  pending approval outlives its run, and the badge has to clear when a
  teammate decides it from anywhere. But two of these surfaces answer a
  question about the whole
  deployment - the worst budget state anywhere, and the size of the whole
  queue - and a session does not stop being over its cap or holding a pending
  request when its last run finishes. So `refreshAllSessions` reads every
  session once, driven by the set of session IDs changing: at first hydration,
  and thereafter only when a session is created or removed. Events never
  trigger it.
- **The heartbeat is narrower still.** It claims only the session in view,
  because presence is keyed on (member, session) and beating them all would
  report you online to teammates in sessions you have never opened.
- **The queue is every session's, in one list.** The inbox view reads them all
  again when it opens, because it is the surface that shows the requests
  themselves rather than a count.
  Decisions go through `approval.decide` with the run the request belongs to,
  so the server attributes them and applies the steer check: a refusal is
  rendered as the server's answer, never predicted by the form. A request the
  user has just decided stays on screen reporting its outcome, laid over the
  fetched queue, because the next fetch no longer returns it.
- **The feed opens at the end of the log.** `session.timeline` pages forward
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
  colour from the member payload.
- **A budget warns, it never stops anything.** The status bar shows the spend
  and the worst state any session is in (`ok`, `warn`, `exceeded`) - every
  session, finished ones included, which is what the wide read above is for -
  and says so in those words. `budget.set` is deliberately off the gateway allowlist, so
  there is no editing UI: budgets are set over SSH. A spend that includes
  unmetered runs renders as a floor (`$1.20+`), because a harness with no
  adapter reports nothing.
- Watcher avatars come from the roster's `watching` set, which the gateway
  fills from live PTY attaches - the browser's attaches included.
- The same refresh reads `GET /api/v1/disk` and writes it onto the stored
  `server.info`, which is what fills the status bar's disk gauge.

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
  derived in `src/lib/status.ts`; the domain status enum is untouched. A
  session row shows the worst state of its runs.
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
rendered against a seeded store: bucket membership and ordering, the ack
muting a card and a later state change bringing the emphasis back, the paused
badge going on and off through a real `session.timeline` pause and resume run
through `applyEvent`, the Idle toggle, a slot contributor reaching the card,
and the palette jumping, steering from a run-detail tab, withholding both pause
and resume while the paused state is unknown, and launching. The team surfaces are driven through the same stub
API: the status bar reading roster, queue and budget and rendering all three,
the approval badge and watcher avatars reaching a real run card, a decision
going out as `approval.decide` and coming back attributed, a steer refusal
surfacing instead of being guessed at, the recurring refresh staying bounded
to live sessions while a finished session's exceeded budget still reaches the
readout, and the feed opening its window at the log head, walking it back
without re-reading, narrowing on a filter, and abandoning a page that belongs
to filters the user has left. The diff tab covers the parser on the
shapes that would break it - a deletion, a new file, a removed line that reads
exactly like a file marker - then the fetch, the truncation notice, a snapshot
refetching and narrowing the patch, and a conflict chip naming its member and
opening their run. Full end-to-end coverage of the dashboard belongs to the
E2E harness driving the real gateway.
