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
the scale, so `cn('text-ui', 'text-muted')` keeps both classes. `text-title`
sets its own family and weight; do not pair it with `font-*` classes. No
surface uses `text-title` yet, so Saira loads and shows only once one does;
until then headings render in Inter.

## Semantic palette

`web/src/index.css` is authoritative. The theme preference is `system`,
`light` or `dark`; `system` follows `prefers-color-scheme` live.

| Token | Utility | Light | Dark | Use |
| --- | --- | --- | --- | --- |
| `--canvas` | `bg-canvas` | `#ffffff` | `#141516` | Main view, fields |
| `--chrome` | `bg-chrome` | `#f7f7f7` | `#1b1c1d` | Sidebar, phone top bar, window bar, code, browser chrome |
| `--hover` | `bg-hover` | `#f3f3f3` | `#1d1e1f` | Hovered rows and cards on canvas |
| `--hover-chrome` | `bg-hover-chrome` | `#ececec` | `#262728` | Hover on chrome and raised surfaces: sidebar and list rows, menu items, ghost and secondary buttons |
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

The five run states (see
[dashboard-frontend.md](dashboard-frontend.md#run-state)), used on dots,
state lines, callouts and diffs only:

| State | Utility | Light | Dark |
| --- | --- | --- | --- |
| Needs you | `state-needs-you` | `#8a5d00` | `#e0a52a` |
| Working | `state-working` | `#1b65c2` | `#4a9eff` |
| Paused | `state-paused` | `#6e6e6e` | `#8a8a8a` |
| Done | `state-done` | `#1a7a36` | `#45c26a` |
| Failed | `state-failed` | `#c8321f` | `#f05c4a` |

Each state has a `-soft` fill (`bg-state-failed-soft`) at 8% light, 12% dark,
for callouts and request cards; `bg-accent-soft` likewise. Diffs use
`text-diff-add`/`text-diff-del` (the done and failed colours) and
`bg-diff-add-bg`/`bg-diff-del-bg` at 8% light, 10% dark.

Agent vendor colours (`text-agent-claude`, `-codex`, `-pi`, `-omp`,
`-opencode`) appear only on the Agents page and the launch picker, through
`AgentGlyph colored`.

`web/src/design-system.test.ts` checks every text tier on every surface, the
accent text, the states and the diff colours for 4.5:1, and control borders,
`--icon-faint` and the agent colours on canvas for 3:1, in both themes.

Member colours are the only arbitrary server data applied inline, on avatars,
attribution rails and the Map's owner boundaries. Identity colour never
replaces run-state colour.

The pre-v2 names (`--background`, `--foreground`, `--card`, `--sidebar`,
`--muted`, `--muted-foreground`, `--primary`, `--border`, `--input`,
`--toolbar-hover`, `--destructive`, `--state-waiting`,
`--state-needs-attention`, `--state-idle`) remain as aliases of these tokens
until every screen is rebuilt; the old hover names (`--accent`, `--secondary`,
`--toolbar-hover`) point at `--hover-chrome`. Do not use them in new code.

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

- One sidebar, 260px by default and resizable from 220px to 400px; under
  768px a 48px top bar and the sidebar as a side sheet. The desktop window
  bar is 35px.
- 44px view headers (`PaneHeader`) and 35px section headers; the run detail's combined tab and action
  strip stays 36px, while dock headers use a `min-h-9` strip whose
  actions can wrap to another row; 28px list rows (44px coarse).
- 28px fields and buttons, 24px small and toolbar icon buttons (44px on a
  coarse pointer), 12px form gaps, 4px label gaps, 16px content gutters and
  12px compact gutters.
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
  badge, a reason line and brief owner/harness/time metadata. A Needs you
  reason reads in the text colour, never with a second dot or a **New** pill.
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

The run Browser uses shared 13px controls at 28px for mouse input and 44px
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

The shell is one sidebar beside the content view, with no icon rail, title
bar, status bar or stacked banners. The sidebar is `bg-chrome` with a seam on
its right; its rows are `ListRow`s with 16px muted icons, the open page in
`bg-selection`. **New run** at its top is the only filled button in the shell.
Run rows carry a shaped `StatusDot`, the title and a monochrome `AgentGlyph`;
a row that does not need the viewer shows its title in `text-muted`, never
with opacity. Group headers are sentence-case 12px muted disclosure buttons
with their count. The footer holds the avatar, name and a connection dot; the
update notice above it is one 12px muted line with an **Update** link.

Under 768px the 48px top bar (`bg-chrome`) holds the sidebar button, with an
amber dot while anything needs the viewer, the view title in 13/20 medium,
**Search** and **New run**. The sidebar opens as a left side sheet. Content
views draw `PaneHeader`: the title in `text-title`, one optional muted line
under it, and actions on the right. A connection problem is a failed-tone
`StateLine`, in the header on desktop and under the top bar on a phone.

The command palette is mounted exactly once in `AppShell` and opened from the
**Search** buttons, `Mod+K` or `Mod+Shift+P`, top-centred, max 600px, with
compact rows. Theme is in **Settings → Appearance** and the footer menu, with
explicit **System**, **Light** and **Dark** choices and matching palette
commands. Appearance works on remote gateways too, while machine-local
settings retain their capability gates.

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
On phones Room is a full-width modal sheet below the top bar, contains
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

## Run state and motion

The `--state-*` tokens in [Semantic palette](#semantic-palette) are the
status vocabulary for a run's presentation state. Domain status enums remain
unchanged.

`--duration-overlay` (120ms) and `--ease-out` are the overlay motion
tokens: menus, popovers, selects, tooltips and dialogs fade and scale in over
it, sheets slide in from their edge, and every one leaves at once. Panels
change instantly. The `state-pulse` dot and the
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
