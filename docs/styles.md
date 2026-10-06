# Styles

## Workbench language

The dashboard is a dense developer workbench on graphite surfaces with one
teal accent. Adjoining panes are flat and quiet: do not add saturated colours,
gradients, blurred cards or elevated nested panels.

## Type

Fonts ship in the bundle; nothing is fetched from Google.

| Face | Use | Source |
| --- | --- | --- |
| Inter (variable, Latin subset) | All UI text | `@fontsource-variable/inter`, one WOFF2 |
| Saira 600 (Latin subset) | `text-title` only | `web/public/fonts/saira-latin.woff2` |
| VT323 | The `aether` wordmark | `web/public/fonts/vt323-latin.woff2` |
| JetBrainsMono NFM | xterm only | `web/public/fonts/jetbrains-mono-nfm-*.woff2` |
| `ui-monospace, 'SF Mono', Menlo, Consolas, monospace` | Code, paths and SHAs (`font-mono`, `--font-code`) | system |

Inter loads with `font-display: swap` behind `Inter Fallback`, Arial scaled
to Inter's metrics, so the swap does not reflow text. Characters outside
Latin render in the fallback.

The type scale, in `web/src/index.css`:

| Class | Size / line height | Use |
| --- | --- | --- |
| `text-ui-xs` | 11/16 | Dense meta |
| `text-ui-sm` | 12/16 | Supporting copy, section labels |
| `text-ui` | 13/20 | Default UI text; the body is 13px |
| `text-prose` | 14/22 | Agent prose and rendered markdown |
| `text-title` | 16/24, Saira 600 | Pane, dialog, step and empty-state headings |

Weights are 400 and 500, 600 for titles. Times, counts and `+a -d` use
`tabular-nums`. No uppercase labels. `cn()` in `web/src/lib/utils.ts` knows
the scale, so `cn('text-ui', 'text-muted')` keeps both classes.

## Semantic palette

`web/src/index.css` is authoritative. The theme preference is `system`,
`light` or `dark`; `system` follows `prefers-color-scheme` live.

| Token | Utility | Light | Dark | Use |
| --- | --- | --- | --- | --- |
| `--canvas` | `bg-canvas` | `#ffffff` | `#141516` | Main view, fields |
| `--chrome` | `bg-chrome` | `#f7f7f7` | `#1b1c1d` | Title bar, sidebar, headers, code, browser chrome |
| `--hover` | `bg-hover` | `#f3f3f3` | `#1d1e1f` | Hovered rows, cards, ghost buttons |
| `--raised` | `bg-raised` | `#ffffff` | `#222324` | Floating surfaces only |
| `--seam` | `border-seam` | black 8% | white 8% | Every border |
| `--control-border` | `border-control` | `#8a8a8a` | `#767676` | Fields, checkboxes, radios |
| `--text` | `text-text` | `#1f1f1f` | `#e8e8e8` | Text |
| `--text-muted` | `text-muted` | `#6b6b6b` | `#9a9a9a` | Placeholders, timestamps, meta |
| `--icon-faint` | `text-icon-faint` | `#8a8a8a` | `#7a7a7a` | Disabled text, decorative icons |
| `--accent-fill` | `bg-accent` | `#367f77` | `#367f77` | The one primary button, focus ring |
| `--accent-text` | `text-accent` | `#306f69` | `#5fb3a8` | Links, accent text |
| `--selection` | `bg-selection` | `#dfebe9` | `#17413d` | Selected rows |

The accent marks selection, focus, the one primary button per view and
links, nothing else. Inside a selected row, secondary text uses `text-text`.
A floating surface is `bg-raised`, a 1px seam and `shadow-overlay`
(`0 8px 24px -12px`, black 18% light, 60% dark).

Run states, used on dots, state lines, callouts and diffs only:

| State | Utility | Light | Dark |
| --- | --- | --- | --- |
| Working | `state-working` | `#1b65c2` | `#4a9eff` |
| Needs you | `state-needs-you` | `#8a5d00` | `#e0a52a` |
| Failed | `state-failed` | `#c8321f` | `#f05c4a` |
| Done | `state-done` | `#1a7a36` | `#45c26a` |
| Paused | `state-paused` | `#6e6e6e` | `#8a8a8a` |

Each state has a `-soft` fill (`bg-state-failed-soft`) at 8% light, 12% dark,
for callouts and request cards; `bg-accent-soft` likewise. Diffs use
`text-diff-add`/`text-diff-del` (the done and failed colours) and
`bg-diff-add-bg`/`bg-diff-del-bg` at 8% light, 10% dark.

`web/src/design-system.test.ts` checks every text tier on every surface, the
accent text, the states and the diff colours for 4.5:1, and control borders
and `--icon-faint` on canvas for 3:1, in both themes.

Member colours are the only arbitrary server data applied inline, on avatars,
attribution rails and the Map's owner boundaries. Identity colour never
replaces run-state colour.

The pre-v2 names (`--background`, `--foreground`, `--card`, `--sidebar`,
`--muted`, `--muted-foreground`, `--primary`, `--border`, `--input`,
`--toolbar-hover`, `--destructive`, `--state-waiting`,
`--state-needs-attention`, `--state-idle` and the HeroUI names) remain as
aliases of these tokens until every screen is rebuilt. Do not use them in new
code.

## Enforcement

`web/src/design-system.test.ts` scans `web/src` and fails on sizes outside
the scale, Tailwind palette colours, `@heroui` imports, `lucide-react`
imports outside `web/src/components/icons.ts`, lucide's alias names
(`XIcon`, `Loader2`), the misspelled `state-attention`, `state-success` and
`state-warn`, bare `<button>`, `opacity-*` in the sidebar and board, and
restyling a `components/ui` primitive through `className`. Each rule carries a legacy list of files that predate it; a
rebuilt file leaves the list, and the test fails if a listed file already
complies.

## Icons

Icons come from `web/src/components/icons.ts`, which re-exports the lucide
icons the dashboard uses at stroke width 1.75. Add a name there rather than
importing `lucide-react`.

## Geometry and responsive behavior

Use compact workbench geometry rather than landing-page ornament:

- 35px title and command bar, 48px activity rail with 24px icons, and a
  preferred 260px workspace/run sidebar constrained to 200-520px.
- 35px view and section headers; the run detail's combined tab and action
  strip stays 36px, while dock headers use a `min-h-9` strip whose
  actions can wrap to another row; 22px status rows and 22-28px list rows
  according to real content.
- 26px fields and buttons, 22px compact tools, 12px form gaps, 4px label
  gaps, 16px content gutters and 12px compact gutters.
  Shared `Label` captions are block-level: stacked caption-to-field spacing
  must measure 4px, including wrapped fields, rather than relying on margins
  on inline text.
- Two radii: `--radius-control` 4px (`rounded-sm`, `rounded-control`) for
  buttons, fields, chips, rows, code and keys; `--radius-panel` 6px
  (`rounded-md`, `rounded-lg`, `rounded-panel`) for cards, dialogs, menus,
  popovers and callouts. `rounded-full` is for dots and avatars only.
- Only floating surfaces cast `shadow-overlay`; content panels have none.
  Headers stay at `text-title` or smaller.
- Scrollbars are 8px with an `--icon-faint` thumb.
- Board Cards use compact, natural-height rows with a full-width title, state
  badge and brief owner/harness/time metadata. Unseen titles stay bold with an
  accessible **Unseen** description, not a second dot or a **New** pill.
  **Details** reveals the branch and copy control along with the full details;
  protection, questions, conflicts and archival state remain visible when relevant.
  Desktop column headers share a subgrid row so card lists start together.
  Map packs fixed-size nodes from `map-layout.ts`, not measured card heights;
  its camera controls live with the **Runs** header rather than in a second
  toolbar. Preserve the gesture, camera and motion contracts in
  [Dashboard SPA: Board](dashboard-frontend.md#board).

At 390px every operation remains available through compact navigation or
overflow, stacked forms, bounded dialogs and tree-to-file navigation. The main
page never gains horizontal overflow; text and code may scroll inside their
own surfaces. Shared buttons, inputs and selectors use 40-44px touch targets
under `coarse:` while retaining desktop density. Floating menus stay inside
the available viewport and scroll to their last action.

The run Browser uses shared 13px controls at 26px for mouse input and 44px
for coarse pointers, including native selects. Before a page is selected,
the URL field and **Open browser** are primary; a selected page adds
Back/Forward/Reload and **Go**. **Browser tools** holds page and viewport
selection, New page, Screenshot, Reconnect and the gated Close page/Reset
session actions. These destructive actions use shared AlertDialog confirmations
bound to the captured page/session and control authority, retain raw failures,
and return focus to Browser tools when dismissed. Groups wrap without
stretching the page.

Run-terminal controller and viewer names stay in the existing toolbar, never
in an extra presence row. Keep all viewers in a horizontally scrollable list.
**Terminal tools** is a compact popover on narrow or touch panes; fine-pointer
panes show inline tools from 42rem, or 70rem when attachment controls share
the toolbar. The run shell strip keeps its own lease control plus **More**
for Screenshot, Hide and confirmed Stop; it does not own the browser or
primary run's lease. The environment dock promotes **Save environment**,
with gated Forward port, Stop environment and Reset to standard in **More**.

Route roots own the shell's bounded height; their content regions use
`min-h-0` and vertical overflow. On phones Diff scrolls its local controls
and patch together, so long fetch output cannot strand the patch. Files
keeps its header actions on a two-column grid, and its touch-sized search
panel scrolls independently without consuming the entire editor.

## Shell, palette and focus

Above 640px the shell keeps a persistent 48px activity rail, with **Work**
and **Workspace** groups, capability gates, accessible labels and tooltips,
and an active 2px indicator. A visibly labeled **More** menu holds destinations
that do not fit the rail's measured available height; there is no fixed
destination-count limit. **Admin** sits at the bottom beside the universally
reachable **Settings** destination. All runs remains available through the
global overview and palette, not as a second Board link in the rail.

The adjacent workspace/run sidebar keeps its persisted splitter behavior and
compact group rows. At 1000px and narrower it collapses into the rail without
changing the stored preference. At 640px and narrower there is no permanent
rail strip: the titlebar's **Expand sidebar** opens a transient modal drawer
containing navigation and runs. **Mod+B** toggles it; closing restores focus,
and navigation or crossing the phone breakpoint resets the drawer.

The browser and Electron titlebar is a real 35px command center. It names the
active workspace and opens the existing palette through `togglePalette(true)`.
Its right side owns the filled **New run** action when connected and launchable,
using the selected workspace; native window controls remain at the far right.
Do not repeat that action in the sidebar or populated Board header; the empty
Board's launch CTA remains. The command palette is mounted exactly once as an
independent AppShell host, never as a hidden status-slot contributor. Quick
input is top-centered directly under the titlebar, max 600px, with compact rows
and no giant scrim-heavy card.

The status Slot remains mounted once for team refresh and other live
contributors, including shortcuts. At every width, secondary facts,
version/storage, presence, budget and errors live in the bounded,
keyboard-reachable status details popup. Connection and shortcuts remain
outside it, with an Approvals signal on phones; do not duplicate Timeline
navigation there. Wrapped readouts use a 1.5 line height; the status bar keeps its
compact row geometry.
Theme discovery belongs in **Settings → Appearance**, with explicit
**System**, **Light** and **Dark** choices and matching palette commands,
not a cycling status icon. Appearance works on remote gateways too, while
machine-local settings retain their capability gates.

`focusRing` and `field` remain signature-compatible shared utilities. Preserve
their keyboard outline, inset behavior for full-bleed rows, readable
placeholders, disabled and read-only states, menu roving focus, typeahead,
portalling and viewport flipping. Shared primitives retain their props,
events, refs and accessibility contracts.

Run headers expose at most two contextual labeled actions plus **More** at
every width; destructive overflow actions remain last, with their existing gates and
confirmations. **Task and details** holds the full task and metadata rather
than a separate per-run Overview tab. Terminal, Browser, Diff and Events remain;
the global overview is unaffected.

On desktop, **Run Room** is a real flex sibling beside the terminal and its
dock, below the run header, capped at 420px or 40% of the available width.
It never overlays that work area. The terminal toolbar is the single source
for same-run controller/presence controls, and protection stays in the header.
On phones Room is a full-width modal sheet below the titlebar, contains
keyboard focus and retains those metadata/control affordances. Closing restores
focus without discarding the draft. One host-owned holder decision dialog
serves the toolbar and phone Room's shared takeover gesture, above Room and
Evidence. While that decision is open, Evidence defers its sheet/popover
layout change so the interrupted surface cannot steal focus.

Within Terminal, **Evidence** has one persistent trigger in the existing dock
header, including when the shell is collapsed or empty, and none in Room.
Desktop Evidence is an anchored popover bounded by the terminal tabpanel;
phones use a modal sheet. Answering with a fact closes Evidence and opens and
focuses a Room comment draft without sending it, preserving attachments and
clearing steer correlation. Retention, expiry, partial-source disclosure and
authority boundaries are unchanged.

## Run state and startup motion

The `--state-*` tokens in [Semantic palette](#semantic-palette) are the
status vocabulary for a run's presentation state. Domain status enums remain
unchanged.

Menus and dialogs animate over `--duration-overlay` (120ms) with
`--ease-out`; panels change instantly. The `state-pulse` dot and the
`live-shimmer` text sweep step through a few frames per cycle rather than
tweening, and stop under `prefers-reduced-motion: reduce`, where the shimmer
leaves plain muted text.

`StateIndicator` uses bouncing dots for working runs in cards, headers and
lists. The labeled run-status chip reserves the full width of all three dots
before its text; compact unlabeled surfaces keep the fixed dot box so state
changes do not shift their columns. Sidebar rows pulse one dot and palette rows
remain static. Live local control has a 1px teal inset outline around the
terminal, alongside the toolbar's **(this tab)** controller marker and
**Release** action. The pointer-transparent outline uses `--accent-fill` without
changing layout. It appears only with live, acknowledged writable control,
outside replay and history reading.
Acquisition and voluntary release propagate along the outline over 720ms,
preserving the former initial speed and decelerating toward the endpoint.
A control-lost takeover uses a 540ms red transition and 1440ms retraction:
1980ms total, three times the former duration.
An occupied five-second hold fills both control buttons diagonally in red and
traces the holder's border red. Text above and below the fill edge uses the
appropriate foreground independently. A seven-second holder decision follows.
Under `prefers-reduced-motion: reduce`, ownership changes are instant,
takeover fills and borders become static red indicators with countdowns, and
working dots and sidebar pulses stop moving; labels retain the state meaning.
Loading spinners and delayed skeletons remain functional feedback.

The desktop first-launch splash is a finite branded handoff, not a loading
screen. Its dark sky, grain, clouds, twinkling field and shooting stars stay
visible for at least 600ms and leave by the 2500ms cap, followed by a 260ms
fade. The mark enters over 700ms. Shooting-star trails use opacity and
`translate3d` only: the recovered 35, 42 and 30 degree trajectories run for
1.35s, 1.55s and 1.7s with staggered entry. The splash is session-only,
unmounts after the fade, and is skipped under
`prefers-reduced-motion: reduce`.

## Accessibility and component contracts

`src/components/ui/heroui.tsx` is the only entry point for the HeroUI v3
`Chip` and `Tooltip` wrappers. Chips carry status and metadata, while run and
dock strips keep their custom manual keyboard semantics. A Tooltip opens on
keyboard focus and pointer hover with a shared 300ms hover delay, points
`aria-describedby` at the control, and supplements rather than replaces an
accessible name. Focusable controls use Tooltip descriptions instead of
`title`; `title` remains for non-focusable paths, timestamps and breakdowns.
Actor marks that need a name use `role="img"` and an `aria-label`, rather than
leaving a named generic span.

Yes-or-no confirmations use the AlertDialog primitive, not a dismissible
Dialog. `AlertDialogAction` is destructive unless the caller says otherwise.
When a confirmation reports a failure, it prevents the default on that click
and closes on the answer instead, so the refusal stays observable rather than
being unmounted with the dialog. Controls that remain reachable while
unavailable use `aria-disabled`, guard their own handlers and retain their tab
stop instead of using `disabled`.

Team status contributions use the 22px compact-row geometry. Presence derives
online members from non-offline roster entries, while watcher marks use the
same named actor avatars; an empty roster or watcher set contributes nothing.
