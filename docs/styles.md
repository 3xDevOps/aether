# Styles

## Workbench language

The dashboard is a dense developer workbench on graphite surfaces with one
teal accent. Adjoining panes are flat and quiet: do not add saturated colours,
gradients, blurred cards or elevated nested panels.
The desktop launch splash is an intentional original-animation exception;
see [Run state and motion](#run-state-and-motion).

## Type

Fonts ship in the bundle; nothing is fetched from Google.

| Face | Use | Source |
| --- | --- | --- |
| DM Sans (variable, Latin subset) | All UI text | `@fontsource-variable/dm-sans`, one WOFF2 |
| Saira (variable, Latin subset) | `text-title` only | `web/public/fonts/saira-latin.woff2` |
| VT323 (Latin subset, 400) | Header and launch-splash wordmarks | `web/public/fonts/vt323-latin.woff2` |
| JetBrainsMono NFM | xterm only | `web/public/fonts/jetbrains-mono-nfm-*.woff2` |
| `ui-monospace, 'SF Mono', Menlo, Consolas, monospace` | Code, paths and SHAs (`font-code`) | system |

DM Sans loads with `font-display: swap` behind `DM Sans Fallback`, Arial
scaled to DM Sans's metrics to reduce layout shift. Characters outside
Latin render in the fallback.

The header wordmark uses `text-wordmark`: VT323 at 24px, 400 weight and line
height 1. The splash keeps its original `clamp(32px, 4vw, 48px)` size and
`0.12em` letter spacing. Both use `--font-pixel`; other headings stay Saira.

The type scale, in `web/src/index.css`:

| Class | Size / line height | Use |
| --- | --- | --- |
| `text-ui-xs` | 11/16 | Dense meta |
| `text-ui-sm` | 12/16 | Supporting copy, section labels |
| `text-ui` | 13/20 | Default UI text; the body is 13px |
| `text-prose` | 14/22 | Agent prose and rendered markdown |
| `text-title` | 16/24, Saira 600 | Pane, dialog, step and empty-state headings |

`text-avatar` (10px) is reserved for avatar initials. **Settings >
Appearance > Text size** (Default, Large, Larger) sets `data-text-size` on
the root, which raises every step of the scale and the title size;
terminals keep their own zoom.

Weights are 400 and 500, 600 for titles. Times, counts and `+a -d` use
`tabular-nums`. Labels are sentence case; nothing is uppercase. `cn()` in
`web/src/lib/utils.ts` knows the scale, so `cn('text-ui', 'text-muted')`
keeps both classes. `text-title` sets its own family and weight; do not pair
it with `font-*` classes.

## Semantic palette

`web/src/index.css` is authoritative: light values sit on `:root`, dark ones
on `.dark`. The theme preference is `system`, `light` or `dark`; `system`
follows `prefers-color-scheme` live.

| Token | Utility | Light | Dark | Use |
| --- | --- | --- | --- | --- |
| `--canvas` | `bg-canvas` | `#ffffff` | `#141516` | Main view, fields |
| `--chrome` | `bg-chrome` | `#f7f7f7` | `#1b1c1d` | Sidebar, phone top bar, window bar, toolbars, code, browser chrome |
| `--hover` | `bg-hover` | `#f3f3f3` | `#1d1e1f` | Hovered rows and cards on canvas |
| `--hover-chrome` | `bg-hover-chrome` | `#ececec` | `#262728` | Hover on chrome and raised surfaces: sidebar and list rows, menu items, ghost and secondary buttons |
| `--raised` | `bg-raised` | `#ffffff` | `#222324` | Floating surfaces, secondary buttons |
| `--seam` | `border-seam` | black 8% | white 8% | Every border; the default border colour |
| `--control-border` | `border-control` | `#8a8a8a` | `#767676` | Fields, checkboxes, radios |
| `--text` | `text-text` | `#1f1f1f` | `#e8e8e8` | Text |
| `--text-muted` | `text-muted` | `#6b6b6b` | `#9a9a9a` | Placeholders, timestamps, meta |
| `--icon-faint` | `text-icon-faint` | `#8a8a8a` | `#7a7a7a` | Disabled text, decorative icons, scrollbar thumbs |
| `--accent-fill` | `bg-accent`, `border-accent` | `#367f77` | `#367f77` | The one primary button, focus ring, control outline |
| `--accent-text` | `text-accent` | `#306f69` | `#5fb3a8` | Links, accent text |
| `--on-accent` | `text-on-accent` | `#ffffff` | `#ffffff` | Text on `bg-accent` |
| `--selection` | `bg-selection` | `#dfebe9` | `#17413d` | Selected rows |

`accent` is a fill for backgrounds and borders but the link colour for text;
`bg-accent-hover` darkens the fill for a hovered primary button. The accent
marks selection, focus, the one primary button per view and links, nothing
else. Inside a selected row, secondary text uses `text-text`. A floating
surface is `bg-raised`, a 1px seam and `shadow-overlay` (`0 8px 24px -12px`,
black 18% light, 60% dark). Dialogs sit over `bg-scrim`.

The five run states (see
[dashboard-frontend.md](dashboard-frontend.md#run-state)), used on dots,
state lines, badges, callouts and request cards:

| State | Utility | Light | Dark |
| --- | --- | --- | --- |
| Needs you | `state-needs-you` | `#8a5d00` | `#e0a52a` |
| Working | `state-working` | `#1b65c2` | `#4a9eff` |
| Paused | `state-paused` | `#6e6e6e` | `#8a8a8a` |
| Done | `state-done` | `#1a7a36` | `#45c26a` |
| Failed | `state-failed` | `#c8321f` | `#f05c4a` |

Each state has a `-soft` fill (`bg-state-failed-soft`) at 8% light, 12% dark;
`bg-accent-soft` likewise. `text-on-failed` is the text on a `bg-state-failed`
fill. Diffs use `text-diff-add`/`text-diff-del` (the done and failed colours)
and `bg-diff-add-bg`/`bg-diff-del-bg` at 8% light, 10% dark.

Agent vendor colours (`text-agent-claude`, `-codex`, `-pi`, `-omp`,
`-opencode`) appear only on the Agents page and the launch picker, through
`AgentGlyph colored`.

Member colours are the only arbitrary server data applied inline: avatar
rings, the owner rail in run lists and member names on conflict chips.
Identity colour never replaces run-state colour.

## Primitives

`web/src/components/ui/` holds the shared primitives: `Button`, `Input`,
`Textarea`, `Select`, `Checkbox`, `Radio`, `Label`, `FormField`, `Menu`,
`Popover`, `Tooltip`, `Dialog`, `AlertDialog`, `Command`, `Tabs`,
`Collapsible`, `Card`, `ListRow`, `PaneHeader`, `SectionLabel`,
`EmptyState`, `Callout`, `Badge`, `RequestCard`, `StatusDot` and
`StateLine`, `Avatar`, `AgentGlyph`, `Code` and `CodeBlock`, `Kbd`,
`Markdown`, `RelativeTime`, `Spinner`, `Skeleton`, `Separator`, `Toaster`,
and the Session view's timeline rows and blocks. Reach for one before writing
markup, and pass its `variant`, `size` or `tone` rather than restyling it.

`Button` variants:

| Variant | Look | Use |
| --- | --- | --- |
| `primary` (default) | `bg-accent`, `text-on-accent` | The one primary action in a view |
| `secondary` | Seam border on `bg-raised` | Other actions |
| `ghost` | Muted text, `bg-hover-chrome` on hover | Toolbar and icon buttons |
| `danger` | `bg-state-failed`, `text-on-failed` | Confirming a destructive action |
| `link` | Accent text, underline on hover | Inline actions that read as links |
| `quiet` | Muted text; `text-text` and an underline on hover | Inline secondary actions inside muted copy |

Sizes are `md` (28px, the default), `sm` (24px), `icon` (28px square) and
`icon-sm` (24px square); each grows to 44px on a coarse pointer. `link` and
`quiet` have no height or padding of their own and widen their touch target
to 44px. An `icon` or `icon-sm` button requires `label`, which becomes its
accessible name and tooltip; `hint` puts a tooltip on any button.

## Enforcement

`web/src/design-system.test.ts` scans every non-test source file under
`web/src` and fails on:

| Rule | Fails on |
| --- | --- |
| Type scale | `text-xs` to `text-9xl`, `text-base`, or an arbitrary size such as `text-[13px]` |
| Token colours | Tailwind palette colours (`bg-gray-100`, `text-white`) or arbitrary hex, `rgb`, `hsl` or `oklch` colours |
| Sentence case | `uppercase`; a group heading is a `SectionLabel` |
| No HeroUI | Any `@heroui/` import |
| Icons through components/icons | A `lucide-react` import outside `web/src/components/icons.ts` |
| One name per icon | lucide aliases such as `XIcon`, `Loader2`, `AlertTriangle` or `MoreHorizontal` |
| No bare buttons | `<button` outside `components/ui/`; use `Button` |
| No opacity on rows | `opacity-*` in `components/shell/` and `routes/board/`; a receding row uses `text-muted` |
| Primitives are not restyled | A primitive given `text-`, `bg-`, `border-`, `rounded-`, `h-`, `min-h-` or padding classes through `className`, which is for layout only |
| Token names | Colour names that are not tokens and render nothing: `background`, `foreground`, `card`, `popover`, `sidebar`, `primary`, `secondary`, `muted-foreground`, `destructive`, `input`, `ring`, `border`, `toolbar-hover`, `field`, and `state-attention`, `-success`, `-warn`, `-waiting`, `-needs-attention`, `-idle` |

A rule can carry a legacy list of files that predate it; only no bare
buttons and primitives are not restyled still have one. The test also fails
when a listed file already complies, so the lists only shrink.

The same file checks contrast in both themes: `text` and `text-muted` on
every surface, `text` on `selection`, accent text on canvas and chrome,
`on-accent` on the accent fill, `on-failed` on the failed fill, and the
states and diff colours on canvas at 4.5:1; control borders, `icon-faint`
and the agent colours on canvas at 3:1. It also checks that every `text-*`
and `bg-*` class in `components/ui/` names a token defined in `index.css`.

## Icons

Icons come from `web/src/components/icons.ts`, which re-exports the lucide
icons the dashboard uses at stroke width 1.75. Add a name there rather than
importing `lucide-react`.

## Geometry and responsive behavior

Use compact workbench geometry rather than landing-page ornament:

- A branded header with horizontal navigation; destinations that do not fit
  move into **More navigation**, except Board and Swarms. One sidebar, 260px
  by default and resizable from 220px to 400px; under 768px it is a side
  sheet, opened from a second, 48px toolbar. The desktop window bar is 35px.
- 44px view headers (`PaneHeader`), 56px for a run's header, 32px terminal
  toolbars, 28px list rows and menu items (44px coarse).
- 28px fields and buttons, 24px small and toolbar icon buttons (44px on a
  coarse pointer), 12px form gaps, 4px label gaps, 16px content gutters and
  12px compact gutters. `Label` captions are block-level, so stacked
  caption-to-field spacing measures 4px, including wrapped fields.
- Two radii: `--radius-control` 4px (`rounded-sm`, `rounded-control`) for
  buttons, fields, chips, rows, code and keys; `--radius-panel` 6px
  (`rounded-md`, `rounded-lg`, `rounded-panel`) for cards, dialogs, menus,
  popovers and callouts. `rounded-full` is for dots, radios and avatars only.
- Only floating surfaces cast `shadow-overlay`; content panels have none.
  Headers stay at `text-title` or smaller.
- Scrollbars are 8px with an `--icon-faint` thumb.
- A board card is three lines: the state line (shaped dot, reason, relative
  time), a title of at most two lines, and the meta line (agent glyph and
  name, owner avatar, `+a −d`, the `card:meta` slot). A Needs you reason reads
  in the text colour, the others in `muted`, never with a second dot or a
  **New** pill. See [Dashboard SPA: Board](dashboard-frontend.md#board).
- Board's **Map** reuses those cards at 320×160 inside a bounded pan/zoom
  canvas. Owner groups use `bg-chrome` and `border-seam`, cards `bg-canvas`;
  identity colour stays on the shared avatars. Muted directed connectors
  link integrators to workers, dashed across owners. The Runs toolbar wraps
  its scope, Mine, archive and camera controls on phones. Fixed geometry
  accommodates the larger text settings and coarse-pointer card actions;
  live presentation updates do not move nodes or reset the camera.

At 390px every operation remains available through compact navigation or
overflow, stacked forms, bounded dialogs and tree-to-file navigation. The main
page never gains horizontal overflow; text and code may scroll inside their
own surfaces. Floating menus stay inside the available viewport and scroll to
their last action. Route roots own the shell's bounded height; their content
regions use `min-h-0` and vertical overflow.

The run Browser view: before a page is selected, one `EmptyState` ("No page
open") with **Open http://localhost:3000** or **Take control**, and **Other
address…** for a URL field; a selected page adds **Back**, **Forward**,
**Reload page** and **Go**. **Page tools** holds page and viewport selection,
**New page**, **Screenshot**, **Reconnect** and the gated **Close page** and
**Reset session**, each behind an `AlertDialog` confirmation that keeps the
raw failure and returns focus to **Page tools** when dismissed.

The terminal toolbar holds the terminal tabs, **Tools** (a menu; a bottom
sheet under 768px) and, at its end, who controls the terminal beside **Take
control** or **Release** ([terminal.md](terminal.md)). A run shell adds
**Shell actions**. The Environment page puts **Save environment** and
**Environment actions** in its header.

## Shell, palette and focus

The shell has a branded navigation header above one sidebar and the content
view, with no icon rail or status bar. The Aether mark and title sit at its
left; Board through Templates follow as horizontal `ListRow`s. Measure
available width rather than assuming every label fits a breakpoint, and
keep Board and Swarms outside the overflow menu. The header and sidebar use
`bg-chrome` with seams against the content; navigation rows have muted icons
and the open page in `bg-selection`. **New run** at the sidebar's top is the
only filled button in the shell.
Run rows carry a shaped `StatusDot`, the title and a monochrome `AgentGlyph`;
a row that does not need the viewer shows its title in `text-muted`, never
with opacity. Group headers are sentence-case 12px muted disclosure buttons
with their count. The footer holds the avatar, name and a connection dot; the
update notice above it is one 12px muted line with an **Update** link.

Under 768px a second, 48px toolbar (`bg-chrome`) holds the sidebar button,
with a needs-you dot while anything needs the viewer, the view title in
13/20 medium, **Search** and **New run**. When the page has its own primary
action, the toolbar's **New run** becomes a ghost `+` icon so the screen keeps
one filled button. The sidebar opens as a left side sheet. Content views draw
`PaneHeader`: the title in `text-title`, one optional muted line under it,
and actions on the right. A connection problem is a failed-tone `StateLine`,
in the header on desktop and under the top bar on a phone.

The command palette is mounted exactly once in `AppShell` and opened from the
**Search** buttons, `Mod+K` or `Mod+Shift+P`, top-centred, max 600px, with
compact rows. Theme is in **Settings > Appearance** and the footer menu, with
explicit **System**, **Light** and **Dark** choices and matching palette
commands.

`focusRing` and `field` in `web/src/lib/utils.ts` are the shared focus and
field styles. Preserve their keyboard outline, inset behavior for full-bleed
rows, readable placeholders, disabled and read-only states, menu roving
focus, typeahead, portalling and viewport flipping. Shared primitives retain
their props, events, refs and accessibility contracts.

The run header shows one primary action for the run's state, a **Details**
toggle and **More**; destructive verbs stay last in **More**, behind their
existing gates and confirmations. The view switch (Session, Terminal,
Changes, Browser) is a segmented tab list. Details is a 320px side panel at
1280px and wider, a side sheet below that and a bottom sheet on phones; it
never overlays the terminal on wide screens. Standard runs keep control in
the terminal toolbar; Enhanced runs keep a compact **Multiplayer** strip
below the view switch on every view. Takeover progress uses `state-failed`
with `on-failed` text in both themes. One run-owned holder decision dialog
sits above every sheet and dialog. Request cards use the needs-you soft fill;
the Session column is 736px wide with no row borders.

## Run state and motion

The `--state-*` tokens in [Semantic palette](#semantic-palette) are the
status vocabulary for a run's presentation state. Domain status enums remain
unchanged.

`--duration-overlay` (120ms) and `--ease-out` are the overlay motion
tokens: menus, popovers, selects, tooltips and dialogs fade and scale in over
it, sheets slide in from their edge, and every one leaves at once. Panels
change instantly. The `state-pulse` dot (the run header's working dot and the
Session view's live row) and the `live-shimmer` text sweep step through a few
frames per cycle rather than tweening, and stop under
`prefers-reduced-motion: reduce`, where the shimmer leaves plain muted text.

The desktop launch splash deliberately retains its original animation,
independent of these workbench motion tokens: the grain, three drifting cloud
layers, stepped starfield and coloured stars, satellites and shooting stars,
700ms logo entrance and 260ms fade. Its status dot uses the local
`launch-status-pulse`: the original 1.8s `ease-in-out` loop from opacity 1
to 0.4 and back, not the shared stepped `state-pulse`. The splash's original
palette and keyframes stay scoped to `launch-splash` in `web/src/index.css`;
do not restyle them to match the workbench. Reduced motion skips the splash
entirely, and its CSS stops animation if the preference changes while it is
visible. [Launch splash](dashboard-frontend.md#launch-splash) defines the
session and startup timing.

Live local control draws a 1px `--accent-fill` outline around the terminal
viewport, alongside the toolbar's **You control** and **Release**. The
outline is pointer-transparent and does not change layout. It appears only
with live, acknowledged writable control, outside replay and history
reading. Acquisition and voluntary release propagate along it over 720ms,
decelerating toward the endpoint. A control-lost takeover turns it red over
540ms and retracts it over 1440ms. An occupied five-second hold fills **Take
control** and the holder's **Release** diagonally in red and traces the
holder's border red; text on either side of the fill edge keeps its own
contrast. A seven-second holder decision follows. Under
`prefers-reduced-motion: reduce`, ownership changes are instant and takeover
fills and borders become static red indicators with countdowns; labels keep
the state meaning. Loading spinners and delayed skeletons remain functional
feedback.
