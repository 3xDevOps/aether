# Styles

## Workbench language

The dashboard is a VS Code-inspired developer workbench, not an official
reusable VS Code component package. It uses the structure, density and neutral
semantics of VS Code Dark Modern and Light Modern while preserving Aether's
routes, run-state colours and capabilities. Adjoining panes are flat and
quiet: do not add saturated accent colours, arbitrary gradients, blurred cards or
elevated nested panels.

The UI uses the native system stack: `system-ui, Ubuntu, Droid Sans, sans-serif`
at 13px with a 1.4 line height; supporting copy is 12px. JetBrainsMono NFM is
retained for terminal output, commands and code. VT323 remains only for the
Aether wordmark and the original startup splash. Do not use the brand face for
body copy.

## Semantic palette

`web/src/index.css` is authoritative. Light and dark are semantic
Light Modern and Dark Modern modes, not separate feature palettes. The theme
preference remains `system`, `light` or `dark`, with `system` following
`prefers-color-scheme` live.

| Token | Light Modern | Dark Modern | Use |
| --- | --- | --- | --- |
| `--background` | `#ffffff` | `#1f1f1f` | Editor and main view |
| `--sidebar` | `#f8f8f8` | `#181818` | Titlebar, activity/sidebar chrome, panel and statusbar |
| `--card` | `#ffffff` | `#1f1f1f` | Main workbench surface |
| `--popover` | `#ffffff` | `#202020` | Actual widgets and floating surfaces |
| `--foreground` | `#3b3b3b` | `#cccccc` | Primary text |
| `--muted-foreground` | `#616161` | `#9d9d9d` | Description and secondary text |
| `--field-placeholder` | `#767676` | `#989898` | Placeholder text |
| `--border` | `#e5e5e5` | `#2b2b2b` | Pane seams and quiet separators |
| `--input` | `#949494` | `#7a7a7a` | Contrast-tuned field boundary |
| `--primary` | `#005fb8` | `#0078d4` | Interaction blue and focus |
| `--primary-hover` | `#0258a8` | `#026ec1` | Primary hover |
| `--toolbar-hover` | `#f2f2f2` | `#2a2d2e` | Flat toolbar and row hover |
| `--selection` | `#e8e8e8` | `#04395e` | Selected rows and text |
| `--selection-foreground` | `#000000` | `#ffffff` | Text on selection |

`@theme` exposes the semantic utilities `bg-selection`,
`text-selection-foreground`, `bg-primary-hover` and `bg-toolbar-hover`
alongside the existing background, sidebar, card and field utilities.
`--input` is intentionally stronger than the reference field-border colours
where needed to keep the boundary discernible against its field surface.
Pane seams retain the quieter Modern values and do not need input-border
contrast. Interaction blue is separate from run status and member attribution.
HeroUI aliases consume these semantics; they do not define a second palette.
Member colours are the only arbitrary server data applied inline, on avatars
and attribution rails while text remains token-based.

## Geometry and responsive behavior

Use compact workbench geometry rather than landing-page ornament:

- 35px title and command bar, 48px activity rail with 24px icons, and a
  preferred 260px workspace/run sidebar constrained to 200-520px.
- 35px view and section headers; 36px run and dock tab strips; 22px status
  rows and 22-28px list rows according to real content.
- 26px fields and buttons, 22px compact tools, 12px form gaps, 4px label
  gaps, 16px content gutters and 12px compact gutters.
- Adjoining panes, sections, rows, run cards and tab strips have zero radius.
  Compact controls and chips use 2px; fields, buttons, popups and dialogs use at
  most 4px. Full circles are reserved for actual avatars, status dots, radio
  controls and switch knobs.
- Radius tokens are limited to 0, 2px and 4px; content panels have no shadows.
  Restrained shadows are limited to actual floating menus, quick input and
  dialogs. Headers stay 13-16px, with no promotional 20-24px titles or
  oversized cards.

At 390px every operation remains available through compact navigation or
overflow, stacked forms, bounded dialogs and tree-to-file navigation. The main
page never gains horizontal overflow; text and code may scroll inside their
own surfaces. Coarse pointer controls may grow to 32px where required.

## Shell, palette and focus

The shell keeps a persistent 48px activity rail for existing navigation, with
capability gates, accessible labels and tooltips, an active 2px indicator and
overflow when destinations do not fit. The adjacent workspace/run sidebar
keeps its persisted splitter behavior and uses compact group rows. At 1000px
and narrower that pane collapses into the persistent rail, which exposes
**Expand sidebar** without changing the stored preference. At 640px and
narrower its expanded pane overlays the center from the rail's right edge.

The browser and Electron titlebar is a real 35px command center. It names the
active workspace and opens the existing palette through `togglePalette(true)`.
The command palette is mounted exactly once as an independent AppShell host,
never as a hidden status-slot contributor. Quick input is top-centered directly
under the titlebar, max 600px, with compact rows and no giant scrim-heavy card.
The status Slot remains mounted once for team refresh and other live
contributors, including shortcuts. At narrow widths secondary status details
use a bounded, keyboard-reachable popup while connection and theme controls stay
available.

`focusRing` and `field` remain signature-compatible shared utilities. Preserve
their keyboard outline, inset behavior for full-bleed rows, readable
placeholders, disabled and read-only states, menu roving focus, typeahead,
portalling and viewport flipping. Shared primitives retain their props,
events, refs and accessibility contracts.

## Run state and startup motion

The six `--state-*` tokens remain the status vocabulary for a run's
presentation state. Domain status enums remain unchanged.

| Token | Light | Dark |
| --- | --- | --- |
| `--state-working` | `#2b6cb0` | `#3794ff` |
| `--state-waiting` | `#8b6c00` | `#cca700` |
| `--state-needs-attention` | `#bc4b00` | `#e2c08d` |
| `--state-failed` | `#c72e0f` | `#f85149` |
| `--state-done` | `#2ea043` | `#89d185` |
| `--state-idle` | `#6e7681` | `#858585` |

`StateIndicator` uses bouncing dots for working runs in cards, headers and
lists. Sidebar rows pulse one dot and palette rows remain static. The steering
signal, working dots and sidebar pulse stop moving under
`prefers-reduced-motion: reduce`; state meaning remains available as text and
labels. Loading spinners and delayed skeletons remain functional feedback.

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
