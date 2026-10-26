# Styles

## Workbench language

The dashboard is a dense developer workbench on graphite surfaces with one
teal accent. Adjoining panes are flat and quiet. Elevation is restrained:
cards and secondary buttons carry a light shadow, floating surfaces a deeper
one, panes none. Do not add saturated colours, gradients or backdrop blur.
The desktop launch splash is an intentional original-animation exception;
see [Run state and motion](#run-state-and-motion).

## Type

Fonts ship in the bundle; nothing is fetched from Google.

| Face | Use | Source |
| --- | --- | --- |
| DM Sans (variable, Latin subset) | All UI text | `@fontsource-variable/dm-sans`, one WOFF2 |
| Saira (variable, Latin subset) | `text-title` only | `web/public/fonts/saira-latin.woff2` |
| VT323 (Latin subset, 400) | Header and launch-splash wordmarks | `web/public/fonts/vt323-latin.woff2` |
| JetBrainsMono NFM | xterm primary font; UI private-use symbols through `Aether Nerd Symbols` | `web/public/fonts/jetbrains-mono-nfm-*.woff2` |
| Noto Sans Symbols 2 | Braille and geometric symbols through `Aether Symbols` | `web/public/fonts/noto-sans-symbols-2-spinners.woff2` |
| `ui-monospace, 'SF Mono', Menlo, Consolas, monospace` | Code, paths and SHAs (`font-code`), after symbol fallbacks | system |

DM Sans loads with `font-display: swap` behind `DM Sans Fallback`, Arial
scaled to DM Sans's metrics to reduce layout shift. Sans, title and code
stacks include two symbol-only fallbacks: `Aether Symbols` covers Geometric
Shapes (`U+25A0–25FF`) and Braille Patterns (`U+2800–28FF`), including OMP's
spinner frames; `Aether Nerd Symbols` covers the Unicode private-use areas
used by agent title icons. The latter reuses the terminal's regular and bold
font files. `unicode-range` keeps both aliases from replacing ordinary text.
Other scripts and symbols outside those ranges still use platform fallbacks.
The aliases use `font-display: swap` and load only when a matching character
appears; xterm keeps its own full-font family and cell metrics.

The header wordmark is lowercase `aether` and uses `text-wordmark`: VT323 at
24px, 400 weight and line height 1. The header logo and wordmark sit 1px above
the row's center to align visually with navigation buttons. The splash keeps
its original `clamp(32px, 4vw, 48px)` size and `0.12em` letter spacing. Both use
`--font-pixel`; other headings stay Saira.

The type scale, in `web/src/index.css`:

| Class | Size / line height | Use |
| --- | --- | --- |
| `text-ui-xs` | 11/16 | Dense meta |
| `text-ui-sm` | 12/16 | Supporting copy, section labels |
| `text-ui` | 13/20 | Default UI text; the body is 13px |
| `text-prose` | 14/22 | Agent prose and rendered markdown |
| `text-title` | 16/24, Saira 600 | Pane, dialog, step and empty-state headings |

`text-avatar` (10px) is reserved for avatar initials. **Settings >
General > Text size** (Default, Large, Larger) sets `data-text-size` on
the root, which raises every step of the scale and the title size;
terminals keep their own zoom.

Weights are 400 and 500, 600 for titles. Times, counts and `+a -d` use
`tabular-nums`. Labels are sentence case; nothing is uppercase, and no
letter-spacing is added. `cn()` in `web/src/lib/utils.ts` knows the scale,
so `cn('text-ui', 'text-muted')` keeps both classes. It also knows the
radius, shadow, easing, animation and duration names below, so a later
`rounded-panel` replaces `rounded-control` and `animate-none` replaces
`animate-overlay-in`. `text-title` sets its own family and weight; do not
pair it with `font-*` classes.

## Semantic palette

`web/src/index.css` is authoritative: light values sit on `:root`, dark ones
on `.dark`. The theme preference is `system`, `light` or `dark`; `system`
follows `prefers-color-scheme` live.

| Token | Utility | Light | Dark | Use |
| --- | --- | --- | --- | --- |
| `--canvas` | `bg-canvas` | `#ffffff` | `#141516` | Main view, fields |
| `--chrome` | `bg-chrome` | `#f7f7f7` | `#1b1c1d` | Sidebar, app header, toolbars, code, browser chrome |
| `--hover` | `bg-hover` | `#f3f3f3` | `#1d1e1f` | Hovered rows and cards on canvas |
| `--hover-chrome` | `bg-hover-chrome` | `#ececec` | `#262728` | Hover on chrome and raised surfaces: sidebar and list rows, menu items, ghost and secondary buttons |
| `--raised` | `bg-raised` | `#ffffff` | `#222324` | Floating surfaces, secondary buttons |
| `--seam` | `border-seam` | black 8% | white 8% | Every border; the default border colour |
| `--control-border` | `border-control` | `#8a8a8a` | `#767676` | Fields, checkboxes, radios |
| `--text` | `text-text` | `#1f1f1f` | `#e8e8e8` | Text |
| `--text-muted` | `text-muted` | `#6b6b6b` | `#9a9a9a` | Placeholders, timestamps, meta |
| `--icon-faint` | `text-icon-faint` | `#8a8a8a` | `#7a7a7a` | Disabled text, decorative icons, scrollbar thumbs, tree connectors |
| `--accent-fill` | `bg-accent`, `border-accent` | `#367f77` | `#367f77` | The one primary button, focus ring, control outline |
| `--accent-text` | `text-accent` | `#306f69` | `#5fb3a8` | Links, accent text |
| `--on-accent` | `text-on-accent` | `#ffffff` | `#ffffff` | Text on `bg-accent` |
| `--selection` | `bg-selection` | `#dfebe9` | `#17413d` | Selected rows |

`accent` is a fill for backgrounds and borders but the link colour for text;
`bg-accent-hover` darkens the fill for a hovered primary button. The accent
marks selection, focus, the one primary button per view and links, nothing
else. Inside a selected row, secondary text uses `text-text`. A floating
surface is `bg-raised`, a 1px seam and `shadow-overlay`; dialogs, the
palette and sheets take `shadow-dialog` and sit over `bg-scrim`.
[Geometry](#geometry-and-responsive-behavior) lists every shadow.

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
`-opencode`) appear only on the Agents page, the launch picker and the head
of a Board card, through `AgentGlyph colored`.

Member colours are the only arbitrary server data applied inline: avatar
rings, the owner rail in run lists, the owner mark on sidebar run rows and
member names on conflict chips.
Identity colour never replaces run-state colour.

## Primitives

`web/src/components/ui/` holds the shared primitives: `Button`, `Input`,
`Textarea`, `Select`, `Checkbox`, `Radio`, `Label`, `FormField`, `Menu`,
`Popover`, `Tooltip`, `Dialog`, `AlertDialog`, `Command`, `Tabs`,
`SlidingIndicator`, `Collapsible`, `Card`, `ListRow`, `PaneHeader`,
`SectionLabel`, `EmptyState`, `Callout`, `Badge`, `CountBadge`,
`RequestCard`, `StatusDot` and `StateLine`, `Avatar`, `AgentGlyph`, `Code`
and `CodeBlock`, `Kbd`, `Markdown`, `RelativeTime`, `Spinner`, `Skeleton`,
`ProgressBar`, `TextSwap` and `IconSwap`, `Separator`, `Toaster`,
`LineGutter` (a code line's clickable number gutter, for the Changes view's
comments) and the Session view's timeline rows and blocks. Reach for one
before writing markup, and pass its `variant`, `size` or `tone` rather than
restyling it.

`Button` variants:

| Variant | Look | Use |
| --- | --- | --- |
| `primary` (default) | `bg-accent`, `text-on-accent` | The one primary action in a view |
| `secondary` | Seam border on `bg-raised`, `shadow-button` | Other actions |
| `ghost` | Muted text, `bg-hover-chrome` on hover | Toolbar and icon buttons |
| `danger` | `bg-state-failed`, `text-on-failed` | Confirming a destructive action |
| `link` | Accent text, underline on hover | Inline actions that read as links |
| `quiet` | Muted text; `text-text` and an underline on hover | Inline secondary actions inside muted copy |

Sizes are `md` (28px, the default), `sm` (24px), `icon` (28px square),
`icon-sm` (24px square), and the composer's send control: `round` (28px
circle) and `pill` (28px, fully rounded); each grows to 44px on a coarse
pointer. `link` and `quiet` have no height or padding of their own and widen
their touch target to 44px. An `icon`, `icon-sm` or `round` button requires
`label`, which becomes its accessible name and tooltip; `hint` puts a tooltip
on any button. Colour, fill, border and shadow fade on the hover tokens. Under
a fine pointer every variant but `link` and `quiet` presses in to
`--scale-press` (0.97); a finger darkens it instead. Joined buttons and the
terminal's hold-to-take control never scale.

The other primitives with motion or a fixed size:

- `Tabs` draws one `SlidingIndicator` per list: the underline look slides a
  2px accent bar, the segmented look a `bg-raised` pill with a seam ring. It
  follows keyboard selection and stays attached in a scrolling strip.
  `SlidingIndicator` serves any other set the same way: items carry
  `data-indicator-key`, and the indicator's parent is the positioned
  container.
- `TextSwap` replaces a word in place, the new copy fading in from a 2px
  blur, and exposes only the incoming copy to assistive technology. Key it
  by the state word, never by a ticking value. `IconSwap` does the same for
  an icon; `useTimedFlag` in `web/src/lib/hooks.ts` holds a confirmation
  such as Copied.
- `CountBadge` is an 18px chip of tabular figures; a count changes at once.
- `ProgressBar` is a 4px track whose fill glides to its value. With
  `role="meter"` or `role="progressbar"` it reports `value` of `max`;
  without one it only draws, for a parent that carries the role.
- `Kbd` is an outlined keycap, 20px (`md`) or 16px (`sm`).
- `Spinner` draws a ring: a track at `--opacity-track` under a quarter arc
  turning every 900ms.
- `Skeleton` sweeps a `bg-hover-chrome` band across `bg-chrome`.

## Charts

The Insights activity charts, heatmap and sparklines are hand-drawn SVG; the
Insights > System charts use recharts through `ChartContainer` in
`web/src/components/ui/chart.tsx`, with recharts' animation off. Marks use
`fill-*` and `stroke-*` token utilities or `currentColor`, never inline
colours. Outcomes use the run state tokens (`state-done`, `state-failed`,
`state-paused`). A neutral count such as runs launched is outlined in
`stroke-icon-faint` on `fill-chrome`, so it never reads as the solid paused
grey. A sequential scale steps one hue: `fill-accent` at 25%, 50%, 75% and
100% over `fill-chrome` for zero. Every colour has a text legend, and every
mark is named by its own label or a summary, so nothing depends on colour
alone. Axis and legend text stays at `text-ui-xs` or `text-ui-sm` in
`text-muted`; bars carry no gradients, shadows or animation. A chart scaled
with `preserveAspectRatio="none"` draws strokes with
`vector-effect: non-scaling-stroke`.

## Lanes

Board's **Lanes** view ([dashboard-frontend.md](dashboard-frontend.md#lanes))
follows the chart rules: flat fills, token strokes with
`vector-effect: non-scaling-stroke`, and a **Legend** popover that names every
fill and link. Every encoding differs by line style or marker as well as
colour.

Bars, `rounded-control`, 24px in a 36px lane (32px in 44px on a coarse
pointer), with 1px between state spans. A filled span is its state's text
colour at `--lane-tint` (16% light, 26% dark), `bg-current/(--lane-tint)`,
so every state reads as a tinted bar in both themes; bar text is `text-text`,
which stays at 4.5:1 or more on every fill (`design-system.test.ts` checks
it). Subagent sub-lanes use the same fills.

| Span | Colour |
| --- | --- |
| Working | `text-state-working`; a live bar adds a 2px `border-state-working` end cap and `live-shimmer` on its activity text |
| Needs you, or Needed attention in history | `text-state-needs-you`; a live bar ends in a `bg-raised` **Needs you** tag with a pulsing dot |
| Paused | `text-state-paused` |
| Ready for review | `text-accent`, matching its dot |
| Done, Failed | `text-state-done`, `text-state-failed`; a finished run's working spans take its outcome's colour |
| Waiting, answered, closed | `text-muted` |
| Starting | hollow, dashed `border-seam` |
| History not loaded | hollow, dotted `border-icon-faint` |

Neighbouring spans that share a fill draw as one. A bar that starts before
the window fades in over its first 32px (a mask, not a colour). Strips (a
folded swarm's workers, folded Finished) are 6px and 8px `bg-current` pills
in the state's text colour, neighbours of one colour drawn as one; each
stretch is at least 1/300 of the window, so a brief failure still shows. A
folded swarm's strip takes the most urgent worker state at each moment, in
the order Needs you, Failed, Working, Ready for review, Paused, Waiting,
Done, Closed, so every worker in the window shows on it.

Links use the categorical series by meaning:

| Link | Colour | Style | Ends |
| --- | --- | --- | --- |
| Integrator launched worker | `text-series-7` | solid 1.5px; dashed `5 3` across owners | arrowhead at the worker's bar |
| Agent message | `text-series-5` | dotted, round caps | dot at the sender; arrowhead at the recipient, filled once acknowledged, hollow before; a chip with the kind glyph and count |
| Submission | `text-state-done` accepted, `text-state-failed` rejected, `text-muted` otherwise | solid 1.25px | open circle at the worker, arrowhead at the integrator, ✓ or ✕ chip |
| Task dependency | `text-muted` | dashed `2 2` S-curve with 16px handles | none |
| File overlap | `text-state-needs-you` | dashed `4 2` bracket at the now line | warning chip with the file count |
| Subagent | `border-icon-faint` | elbow tree connectors in the label column | none |

Links and their chips draw under the bars and labels, over the hover and
selection fills; only their 10px hit paths sit above. Hovering or selecting a
lane keeps its links in colour and turns every other link `text-icon-faint`;
nothing uses opacity. The axis is 28px with `text-ui-xs` muted tabular labels
over dashed `border-seam` gridlines, dropping any label within 48px of the
now chip; the now line is 1px `bg-icon-faint` under a neutral `bg-raised`
**now** chip, never accent. The label column is 240px, 48px under 768px. Lanes that change
section glide 220ms on `--ease-enter`; new lanes fade in over 250ms, staggered
by `--stagger-row` up to the eighth. Under reduced motion the pulse, shimmer,
glide and fade stop.

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
| Token names | Colour names that are not tokens and render nothing: `background`, `foreground`, `card`, `popover`, `sidebar`, `primary`, `secondary`, `muted-foreground`, `destructive`, `input`, `ring`, `border`, `toolbar-hover`, `field`, and `state-attention`, `-success`, `-warn`, `-waiting`, `-needs-attention`, `-idle` |

A rule can carry a legacy list of files that predate it; only no bare
buttons still has one. The test also fails
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
importing `lucide-react`. The same module holds `Claude`, the Claude mark that
`AgentGlyph` draws for the `claude` agent; it is filled, not stroked.

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
- Four radii: `--radius-chip` 5px (`rounded-chip`) for badges, counts,
  keys, inline code and checkboxes; `--radius-control` 8px (`rounded-sm`,
  `rounded-control`) for buttons, fields, rows, menu items and tooltips;
  `--radius-panel` 12px (`rounded-md` and up, `rounded-panel`) for cards,
  callouts, dialogs, menus, popovers, toasts and the palette; and
  `--radius-composer` 14px (`rounded-composer`) for the run composer and
  Board's column wells. A radius nested
  in another is the outer one minus the inset: a 12px menu with 4px padding
  holds 8px items. Edges that meet another surface stay square: sheets at the
  screen edge, a run's details box where it docks, joined buttons and dock
  tabs. `rounded-full` is for dots, radios, avatars, meters and the
  composer's send control only.
- Elevation: panes and headers have none; cards `shadow-card`, lifting to
  `shadow-card-hover` on hover; secondary buttons `shadow-button`; menus,
  popovers, selects, tooltips, toasts and the run details box
  `shadow-overlay`; dialogs, the palette and sheets `shadow-dialog`. Values
  differ per theme in `web/src/index.css`. Every shadow sits over a seam
  border, which forced-colors mode keeps when it drops shadows. A shadow
  that must read as none is written `0 0 #0000`, never `none`, so it still
  composes with ring utilities. Headers stay at `text-title` or smaller.
- Scrollbars are 8px with an `--icon-faint` thumb.
- A Board column is a `bg-chrome` well, 8px from the next, with a 36px
  header (sentence-case label and `CountBadge`) and a 3px inset around
  cards 4px apart. Its cards pass `raised` to `Card`: `bg-raised`, keeping
  that fill and lifting the shadow on hover. A card is a head
  (coloured agent glyph, title of at most two lines), a description of at
  most two lines in `text-ui-sm` (one line for live Working activity), and
  a 16px footer whose 24px action overhangs it (dot, label, compact
  age, workspace; then agent name, owner avatar, `+a −d`, the `card:meta`
  slot and the action). A Needs you reason reads in the text colour, the
  others in `muted`, never with a second dot or a **New** pill. From 365px
  of card width (`@container`) the description and footer indent under the
  title. See [Dashboard SPA: Board](dashboard-frontend.md#board).
- Board's **Map** draws its own compact cards (288×128 by default, larger
  for the larger text sizes, 20px taller on a coarse pointer) on a
  `bg-chrome` canvas: `bg-canvas` with `shadow-card`, a 22px `bg-chrome` agent
  tile, the state as a soft `Badge` led by its `StatusDot`, and no pulse.
  Swarms are org charts joined by 1.5px `icon-faint` connectors with rounded
  corners and an 8px ringed dot under the integrator. Owner sections, when
  there are two or more, and Needs you elsewhere are `border-seam` frames
  with no fill; identity colour stays on the shared avatars. The **N needs
  you** and **N failed** chips are small buttons on their state's soft fill,
  a 20% mix of the state colour on hover and while open. The Runs toolbar
  wraps its counts, scope, Mine, archive and camera controls on phones. Live
  presentation updates do not move nodes or reset the camera.

At 390px every operation remains available through compact navigation or
overflow, stacked forms, bounded dialogs and tree-to-file navigation. The main
page never gains horizontal overflow; text and code may scroll inside their
own surfaces. Floating menus stay inside the available viewport and scroll to
their last action. Route roots own the shell's bounded height; their content
regions use `min-h-0` and vertical overflow.

The run Browser view has one `bg-chrome` toolbar row over the page: ghost
icon buttons for **Back**, **Forward** and **Reload**, the address field,
**Ports** with its count, **Viewport**, the beside-or-tab toggle and
**Browser actions**, then who is driving. A listening port is a `secondary`
button, its address in `font-code` and its process name muted, with a ghost
icon button beside it. A second page adds a row of small tab buttons. Messages are laid
over the top of the page on a floating surface and never take a row. A page
that does not fill its pane is centred on `bg-chrome` and never enlarged, and
the page that has the keyboard wears a 1px inset accent outline. Beside
another view the Browser is a pane with a seam and a resize handle like the
sidebar's. [Browser view](dashboard-frontend.md#browser-view) has the
behaviour.

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
Run rows carry a shaped `StatusDot`, a 6×7px owner mark (a right-pointing
triangle in the owner's colour, 4px from the dot and from the title), the
agent's title, a compact age ("15m") in `text-ui-sm` muted tabular figures
and a monochrome `AgentGlyph`. The people on a run follow as 16px avatars
overlapping by 6px, the first one whole at the right edge, each ringed in
the row's own background so the one in front cuts into the one behind.
A swarm's root also has a `Waypoints` graph-node button before the age; it
opens swarm controls while the row opens the run. Use
the same icon for swarm navigation and commands. Swarm children are
indented 26px under the root and joined to it by 1px `icon-faint` tree
connectors: a trunk under the centre of the root's status dot, and for each
child a wire from the trunk to the row's left edge, level with the child's
dot. The trunk ends at the last child's wire. Connectors stay in the indent,
outside every row's hover, selection and focus box. A row that recedes shows
its title in `text-muted`, never with opacity; a finished row no member has
opened yet keeps `text-text` (see
[dashboard-frontend.md](dashboard-frontend.md#sidebar)). A row's details box
is a 288px floating surface docked flush to the row's right edge, its
top-left corner square where it meets the row. Group headers
are sentence-case 12px muted disclosure buttons with their count. The footer
holds the avatar, name and a connection dot; the
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
32px rows (44px on a coarse pointer). Theme is in **Settings > General** and the footer menu, with
explicit **System**, **Light** and **Dark** choices and matching palette
commands.

`focusRing`, `fieldFocus` and `field` in `web/src/lib/utils.ts` are the shared
focus and field styles. Buttons and navigation show their outlines only
after Tab, never after a click or another key; text fields use a 1px inset accent outline over the existing border with a
faint 4px glow and no offset gap. The outline remains visible in forced-colors
mode, where box shadows disappear. The Session composer draws this treatment
on its enclosing box, not its embedded textarea, so attachments do not clip
the highlight. Preserve readable placeholders, disabled and read-only states,
menu roving focus, typeahead, portalling and viewport flipping. Shared
primitives retain their props, events, refs and accessibility contracts.

The run header shows one primary action for the run's state, a **Details**
toggle and **More**; destructive verbs stay last in **More**, behind their
existing gates and confirmations. The view switch (Session, Terminal,
Changes, Browser) is a segmented tab list. A run with a pull request ends
that row with its number as a muted link and its check state as a ghost
button led by a `StatusDot`: failed, working, done or neutral. Details is a 320px side panel at
1280px and wider, a side sheet below that and a bottom sheet on phones; it
never overlays the terminal on wide screens. Standard runs keep control in
the terminal toolbar; Enhanced runs keep a compact **Multiplayer** strip
below the view switch on every view. Names and member avatars identify the
controller and watchers directly in that strip, which stays one row while
the controls fit beside a 6rem roster and otherwise wraps them to a second
line. The roster scrolls horizontally, and below 34rem watchers show as avatars and
below 30rem **Multiplayer** as its icon.
Takeover progress uses `state-failed` with `on-failed` text in both themes.
One run-owned holder decision dialog sits above every sheet and dialog.
Request cards use the needs-you soft fill; the Session transcript column is
736px wide with no row borders. Its composer fills the available pane with
balanced 16px side gutters, including when Details is open. An empty ACP
conversation shows a subdued Aether mark above the centered run title and
metadata rows: workspace, agent, branch, owner and creation time. Symmetric
scrollbar gutters keep the overview centered when its content needs scrolling.

## Run state and motion

The `--state-*` tokens in [Semantic palette](#semantic-palette) are the
status vocabulary for a run's presentation state. Domain status enums remain
unchanged.

Motion confirms a change the viewer caused or marks the one live thing; it
never decorates still data. `web/src/motion.css` holds the tokens:

| Token | Value | Use |
| --- | --- | --- |
| `--duration-hover`, `--ease-hover` | 150ms, CSS `ease` | Colour, fill, border and shadow fades; the default for a bare `transition-*` |
| `--duration-press`, `--duration-release`, `--ease-press` | 150ms, 130ms | Button press scale, `--scale-press` (0.97) |
| `--duration-popover`, `--ease-enter` | 130ms | Menus, popovers, selects and tooltips growing from `--scale-popover` (0.98) |
| `--duration-glide`, `--ease-glide` | 300ms | Tab indicators, meters and layout glides |
| `--duration-expand` | 250ms | Disclosures opening |
| `--duration-pulse` | 1600ms | The live shimmer's period |
| `--duration-instant` | 0ms | Swaps with no fade |

Raw steps (`--duration-100` to `--duration-1600`) cover the rest: 100ms row,
menu-item and tab-label fades; 200ms chevron turns, word swaps, dialog
entrances, scrim fades and disclosures closing; 220ms sheets on
`--ease-sheet`; 360ms row entrances; 1000ms the Working pulse. Each role has
a `duration-<role>` utility that also sets `--tw-duration`, so a later
`transition-*` cannot reset it. `web/src/lib/motion.ts` reads the same tokens
for script animations (`play`, `playEntrance`); code names a token, never a
literal time or curve.

- Overlays fade and grow from their trigger (the Radix transform origin) and
  leave at once. A closing tooltip must not animate, or Radix keeps it above
  the layer the next Escape is meant for. A tooltip opens 250ms after hover,
  at once on keyboard focus, and at once within 300ms of another closing.
- Dialogs fade in from `--scale-enter` (0.95) through `transform`, leaving
  the `translate` that centres them alone. Sheets slide in from their edge; a
  run's details box wipes in from the row it docks to.
- `Collapsible` bodies glide their height, clipped only while they move, and
  unmount once shut.
- Only rows added after the first render enter: `animate-entrance` fades in
  and rises 4px. `playEntrance` staggers a burst by `--stagger-row` (30ms) up
  to the eighth row; the rest arrive with it.
- `web/src/lib/flip.ts` glides reordered items, translating only, for short
  lists the viewer reorders; never a virtualized list. The one store-driven
  use is Board moving at most eight cards between columns; see
  [Board](dashboard-frontend.md#board).
- The Map repacks with motion: units and cards glide from their old layout
  coordinates on `--duration-glide` / `--ease-glide` (never DOM-measured,
  since the world is scaled), new cards rise 6px and fade in over 220ms after
  a 150ms hold, staggered by `--stagger-row` up to the eighth, and removed
  cards fade out over 130ms. Folding fades workers out last to first before
  the repack. Hydration and scope changes never animate.
- Row selection, focus outlines and exits change at once.
- `state-pulse` (the run header's working dot, the Session view's live row
  and the working rows on a swarm's Tasks list) breathes: opacity to
  `--opacity-muted` and size to two thirds and back each second.
  `live-shimmer`, on those rows' reasons and on a working lane's activity in
  Board's Lanes, sweeps a light band across muted text in 1.4s of each 1.6s.
  A Needs you tag in Lanes pulses its dot, never its text. Sidebar rows,
  board cards, the palette and All workspaces never pulse.

Under `prefers-reduced-motion: reduce` every animation and transition stops:
overlays and sheets appear, indicators and bodies jump, the pulse holds, the
shimmer leaves plain muted text, the spinner shows a still arc and a
skeleton a flat fill. Holds and timers do not change. `useReducedMotion()` and `prefersReducedMotion()` in
`web/src/lib/hooks.ts` answer the query in scripts.

The desktop launch splash deliberately retains its original animation,
independent of these workbench motion tokens: the grain, three drifting cloud
layers, stepped starfield and coloured stars, satellites and shooting stars,
700ms logo entrance and 260ms fade. Its status dot uses the local
`launch-status-pulse`: the original 1.8s `ease-in-out` loop from opacity 1
to 0.4 and back, not the shared `state-pulse`. The splash's original
palette and keyframes stay scoped to `launch-splash` in `web/src/index.css`;
do not restyle them to match the workbench. Reduced motion skips the splash
entirely, and its CSS stops animation if the preference changes while it is
visible. [Launch splash](dashboard-frontend.md#launch-splash) defines the
session and startup timing.

Live local control lights the top edge of the terminal viewport or the
Enhanced Session conversation, just under the toolbar; the other edges stay
dark. The light is a uniform bar across the whole edge: a 1.5px
`--control-light` rim with a tight halo and a soft glow spilling down. It is
at 70% strength while it moves or turns red. Once acquisition settles it
dims to half over 1.2s, then breathes between 42% and 60% on an 8-second
cycle. `--control-light` is the accent teal in light
theme and a brighter teal in dark; `--control-glow` dims the glow to 0.3 in
light theme. **Release**, shown while you control, has the accent fill;
**Take control** is a secondary button. The light is pointer-transparent and
does not change layout. It appears only with live, acknowledged writable
control. Terminal replay and history reading hide it; reading earlier
Enhanced items does not disable the composer or hide its light. The right
end, under **Take control** and **Release**, is the light's source:
acquisition pours it leftward across the edge and voluntary release drains
it back to the right, each over 720ms and decelerating toward the endpoint.
A control-lost takeover turns the light red over 540ms and drains it to the
right over 1440ms. An occupied three-second hold fills **Take control** and
the holder's **Release** diagonally in red and spreads red light leftward
from the right end of the holder's edge, replacing the teal it reaches; text
on either side of the fill edge keeps its own contrast. The fill decelerates (logarithmic) while the countdown stays linear. A seven-second holder
decision follows. Under `prefers-reduced-motion: reduce`, ownership changes
are instant, the settled light holds at half without breathing, and takeover
fills and light become static red indicators with countdowns; labels keep the state meaning. Loading spinners and delayed skeletons remain functional
feedback.
