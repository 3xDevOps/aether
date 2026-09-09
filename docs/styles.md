# Styles

## Landing foundations

| Token | Value | Use |
| --- | --- | --- |
| Ink | `#05070f` | Deepest background |
| Panel | `#070b16` | Cards and popovers |
| Bar | `#0b1120` | Bars and raised surfaces |
| Text | `#dce6f2` | Primary text |
| Mint | `#6ee7d6` | Landing accent; the app dims it to `#5cb7aa` for interactive fills |
| Steel | `#4a6fa5` | Dark borders, at 28% alpha (`--border`) and 36% (`--input`) |
| Fonts | VT323, JetBrainsMono NFM | VT323 draws the `aether` wordmark in the title bar and the launch splash, nothing else; JetBrainsMono NFM is the terminal font. The rest of the UI uses the platform sans stack |
| Geometry | `--radius: 0.25rem` | `index.css` derives `--radius-sm` (0), `--radius-md` (2px) and `--radius-lg` (4px) from it; `rounded-md` is the usual surface radius. Round marks take `rounded-full`, and `index.css` writes `border-radius: 999px` where it draws one itself. `rounded-xl` and `rounded-xs` name no token here, so they keep Tailwind's own values (`components/connection-error.tsx`, `components/ui/dialog.tsx`) |
| Launch splash | Sky gradient, grain, clouds, twinkling stars, glows, amber `#ff9d6b`, violet `#a78bfa` | The full-screen splash the SPA shows at startup (`.launch-splash` in `index.css`); skipped under `prefers-reduced-motion` |
| Not ported | Scanlines, and VT323 for body text | Deliberate scope boundary |

## App tokens

`web/src/index.css` defines every colour the dashboard uses; components reach
them through token classes and carry no hex literals. Member colour is the
exception: the six swatches a member picks from are a hard-coded constant in
`web/src/routes/members/index.tsx`, the chosen colour comes back from the
server on the member record, and it is applied inline wherever a member is
attributed - run rows in the sidebar and the run list, board cards and
avatars, the run detail, feed entries, approval rows and conflict chips.
Light is the shadcn neutral base, dark is the landing palette. The theme
control cycles system, light, and dark.

| Token | Light | Dark |
| --- | --- | --- |
| `--radius` | `0.25rem` | `0.25rem` |
| `--background` | `oklch(1 0 0)` | `#05070f` |
| `--foreground` | `oklch(0.145 0 0)` | `#dce6f2` |
| `--card` | `oklch(1 0 0)` | `#070b16` |
| `--card-foreground` | `oklch(0.145 0 0)` | `#dce6f2` |
| `--popover` | `oklch(1 0 0)` | `#070b16` |
| `--popover-foreground` | `oklch(0.145 0 0)` | `#dce6f2` |
| `--primary` | `oklch(0.55 0.11 175)` | `#5cb7aa` |
| `--primary-foreground` | `oklch(0.985 0 0)` | `#05070f` |
| `--sidebar` | `oklch(0.97 0 0)` | `#0b1120` |
| `--secondary` | `oklch(0.97 0 0)` | `#0b1120` |
| `--secondary-foreground` | `oklch(0.205 0 0)` | `#dce6f2` |
| `--muted` | `oklch(0.97 0 0)` | `#0b1120` |
| `--muted-foreground` | `oklch(0.556 0 0)` | `rgb(220 230 242 / 0.62)` |
| `--accent` | `oklch(0.97 0 0)` | `#242e40` |
| `--accent-foreground` | `oklch(0.205 0 0)` | `#dce6f2` |
| `--destructive` | `oklch(0.577 0.245 27.325)` | `oklch(0.704 0.191 22.216)` |
| `--destructive-foreground` | `oklch(0.985 0 0)` | `oklch(0.985 0 0)` |
| `--border` | `oklch(0.922 0 0)` | `rgb(74 111 165 / 0.28)` |
| `--input` | `oklch(0.922 0 0)` | `rgb(74 111 165 / 0.36)` |
| `--ring` | `oklch(0.55 0.11 175)` | `#5cb7aa` |
| `--scrollbar-thumb` | `oklch(0.63 0.08 190 / 0.62)` | `oklch(0.73 0.12 190 / 0.58)` |
| `--scrollbar-thumb-hover` | `oklch(0.55 0.12 190 / 0.85)` | `oklch(0.78 0.13 190 / 0.86)` |
| `--scrollbar-track` | `oklch(0.95 0.01 210 / 0.42)` | `oklch(0.2 0.03 220 / 0.62)` |

## Run state tokens

The six `--state-*` tokens are the status vocabulary for a run's presentation
state. `@theme` re-exports them, so components read them as `bg-state-*` and
`text-state-*` utilities and `.working-dots` reads `--state-working` directly
as its `color`. Changing a status colour is one edit either way.

| Token | Light | Dark |
| --- | --- | --- |
| `--state-working` | `oklch(0.62 0.15 250)` | `oklch(0.7 0.14 250)` |
| `--state-waiting` | `oklch(0.72 0.13 85)` | `oklch(0.8 0.12 85)` |
| `--state-needs-attention` | `oklch(0.65 0.19 35)` | `oklch(0.72 0.17 35)` |
| `--state-failed` | `oklch(0.58 0.22 27)` | `oklch(0.68 0.2 25)` |
| `--state-done` | `oklch(0.62 0.13 155)` | `oklch(0.72 0.12 155)` |
| `--state-idle` | `oklch(0.7 0 0)` | `oklch(0.55 0 0)` |

`--state-working` also drives the working indicator; the Styleguide section of
[dashboard-frontend.md](dashboard-frontend.md) has the rule.

Rows carry a 2px left border, and the two kinds of row spend it differently.
The sidebar's nav items use it for selection: the active one is `bg-accent`
with `border-primary`. Run rows use it for attribution - `shell/sidebar.tsx`
and `run-list.tsx` both set `borderLeftColor` inline from the owner's colour,
whether the row is selected or not - so a selected sidebar run row is marked
by `bg-accent` alone, and the run list has no selected state.
