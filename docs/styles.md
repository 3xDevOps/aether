# Styles

## Design language

The dashboard uses one cool-neutral palette in both themes with a mint primary
accent. Surfaces are quiet and structured rather than decorative: semantic
tokens describe backgrounds, borders, fields, overlays and run states, while
components use those tokens through Tailwind utilities and the shared shadcn
primitives.

Geist is the body and UI face, loaded locally through `next/font/local` as the
`--font-geist-sans` variable. JetBrainsMono NFM is used for terminal output,
commands and code. VT323 is reserved for the Aether wordmark. The base body
size is 14px with a 1.4 line height; supporting copy is usually 13px and
section titles are typically 20-24px. Do not use the pixel face for body copy.

Spacing follows Tailwind's 4px unit (`--spacing: 0.25rem`). The shared radius
scale is 4, 6, 8, 12, 16, 20, 24 and 32px (`xs` through `4xl`), with 8px
(`--radius` and `md`) as the ordinary surface radius. Compact controls and
metadata use the smaller steps; dialogs and major panels may use the larger
steps. Full pills use `rounded-full`.

View headers and page bodies share 16px horizontal gutters, increasing to
24px at `sm`. Header actions move below the title and context in narrow panes.
Inputs and selects are 36px high; textareas retain their row height.
Scrollable dialog forms reserve equal space on both sides for focus
outlines without shifting fields away from their header and footer.

## App tokens

`web/src/index.css` is authoritative. Light and dark are the same semantic
system, not separate palettes. The theme preference cycles `system`, `light`
and `dark`; `system` follows `prefers-color-scheme` live.

| Token | Light | Dark |
| --- | --- | --- |
| `--radius` | `0.5rem` | `0.5rem` |
| `--background` | `oklch(0.985 0.004 240)` | `oklch(0.14 0.012 240)` |
| `--foreground` | `oklch(0.2 0.015 240)` | `oklch(0.93 0.012 240)` |
| `--card` | `oklch(0.998 0.002 240)` | `oklch(0.175 0.014 240)` |
| `--popover` | `oklch(0.998 0.002 240)` | `oklch(0.19 0.014 240)` |
| `--primary` | `oklch(0.49 0.105 174)` | `oklch(0.78 0.12 174)` |
| `--primary-foreground` | `oklch(0.99 0 0)` | `oklch(0.16 0.018 174)` |
| `--sidebar` | `oklch(0.965 0.008 240)` | `oklch(0.155 0.013 240)` |
| `--secondary` / `--muted` | `oklch(0.948 0.01 240)` | `oklch(0.205 0.015 240)` |
| `--muted-foreground` | `oklch(0.43 0.02 240)` | `oklch(0.74 0.015 240)` |
| `--accent` | `oklch(0.925 0.014 240)` | `oklch(0.25 0.018 240)` |
| `--destructive` | `oklch(0.56 0.19 26)` | `oklch(0.66 0.19 27)` |
| `--border` | `oklch(0.87 0.015 240)` | `oklch(0.31 0.018 240)` |
| `--input` | `oklch(0.63 0.02 240)` | `oklch(0.51 0.018 240)` |
| `--scrim` | `rgb(0 0 0 / 0.56)` | `rgb(0 0 0 / 0.56)` |
| `--ring` | `oklch(0.49 0.105 174)` | `oklch(0.78 0.12 174)` |

`--input` is the shared boundary token for inputs, selects and textareas. Its
OKLCH lightness is tuned independently from the quieter `--border`: conversion
to sRGB and WCAG relative luminance gives ratios of 3.34:1 and 3.47:1 in light
mode against `--background` and `--card`, and 3.47:1 and 3.31:1 in dark mode.
The `system` preference resolves to the corresponding light or dark token.
Dialog overlays use the theme-independent `--scrim` token, a black 56%
scrim in both modes, so a light foreground never washes out dark surfaces.

Card, popover, secondary, muted and accent foregrounds follow
`--foreground` unless a component needs a contrast-specific value. The same
file exports HeroUI-compatible aliases for default, overlay, segment, focus,
separator, field and soft success, warning and danger surfaces. Those aliases
point at the shadcn tokens rather than defining a second colour system.

Scrollbars use semantic `--scrollbar-thumb`, `--scrollbar-thumb-hover` and
`--scrollbar-track` values. Member colours are the only arbitrary server data
applied inline: avatars and attribution rails use the member record's colour,
while text stays in the foreground token.

## Run state tokens

The six `--state-*` tokens are the status vocabulary for a run's presentation
state. `@theme` re-exports them as `bg-state-*` and `text-state-*` utilities;
the working indicator reads `--state-working` directly. Domain status enums
remain unchanged.

| Token | Light | Dark |
| --- | --- | --- |
| `--state-working` | `oklch(0.62 0.15 250)` | `oklch(0.7 0.14 250)` |
| `--state-waiting` | `oklch(0.72 0.13 85)` | `oklch(0.8 0.12 85)` |
| `--state-needs-attention` | `oklch(0.65 0.19 35)` | `oklch(0.72 0.17 35)` |
| `--state-failed` | `oklch(0.58 0.22 27)` | `oklch(0.68 0.2 25)` |
| `--state-done` | `oklch(0.62 0.13 155)` | `oklch(0.72 0.12 155)` |
| `--state-idle` | `oklch(0.62 0 0)` | `oklch(0.58 0 0)` |

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

## Component roles

`src/components/ui/` contains the shadcn primitives, tuned to the shared
spacing, radius, field and focus tokens. `src/components/ui/heroui.tsx` is the
only dashboard entry point for HeroUI v3 wrappers: use `Tabs` for component
tab panels, `Chip` for status or metadata, and `Tooltip` for supplemental
hover and keyboard help. The wrapper styles import only the HeroUI tabs, chip
and tooltip layers and map them to the dashboard tokens.

Run and dock tab strips intentionally keep their custom manual tab semantics
because they coordinate route changes, focus handoff and overflow behavior.
They are not replaced by the HeroUI Tabs wrapper. Use the shared `focusRing`
utility for an immediate keyboard outline, without a colour transition, and
use semantic tokens instead of route-specific colour literals.
