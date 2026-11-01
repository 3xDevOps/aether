import type * as React from 'react'
import { Ellipsis } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { Tooltip } from '@/components/ui/tooltip'
import { money } from '@/lib/format'
import { cn } from '@/lib/utils'
import { spanDuration, trackX } from '@/routes/run/trace-row'
import type { CostTotals, Span, TraceStats } from '@/store/trace'
import { axisAt, type Axis, type AxisMark } from '@/store/trace-axis'

/** Tree pane width and the gap right of the track, wide and stacked. */
export const treeWidth = 256
export const trackGap = 16
export const stackedInset = 10

const tokens = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 })

function Stat({ label, children, style }: { label: string; children: React.ReactNode; style?: React.CSSProperties }) {
  return (
    <div className="flex min-w-0 flex-col justify-center gap-1 bg-canvas px-4 py-2.5" style={style}>
      <dt className="truncate text-ui-xs text-muted">{label}</dt>
      <dd className="truncate text-ui text-text tabular-nums">{children}</dd>
    </div>
  )
}

export function TraceStatsStrip({
  root,
  stats,
  cost,
  costError,
  costCapped,
  partial,
  live,
  now,
  wide,
  onErrors,
}: {
  root: Span
  stats: TraceStats
  cost: CostTotals | undefined
  costError: string | null | undefined
  /** The status and cost history read stopped at its cap, so tokens and cost are floors. */
  costCapped: boolean
  /** Earlier history is not held, so counts are floors. */
  partial: boolean
  live: boolean
  now: number
  wide: boolean
  onErrors: () => void
}) {
  const floor = partial ? '≥' : ''
  const cells: [string, React.ReactNode][] = [
    ['Duration', <span className={cn(live && 'live-shimmer')}>{spanDuration(root, now)}</span>],
    ['Turns', `${floor}${stats.turns}`],
    ['Tool calls', `${floor}${stats.tools}`],
  ]
  if (stats.subagents > 0) cells.push(['Subagents', `${floor}${stats.subagents}`])
  cells.push(['Errors', stats.errors.length > 0 ? (
    <Button
      variant="quiet"
      className="text-ui text-state-failed"
      hint="Show the next failure"
      aria-label={`${floor}${stats.errors.length} ${stats.errors.length === 1 ? 'error' : 'errors'}, show the next failure`}
      onClick={onErrors}
    >
      {floor}{stats.errors.length}
    </Button>
  ) : `${floor}0`])
  if (costError) {
    cells.push(['Cost', <Tooltip content={costError}><span className="text-state-failed">Not read</span></Tooltip>])
  } else if (cost) {
    const costFloor = costCapped ? '≥' : ''
    cells.push(['Tokens', (
      <Tooltip content={`${tokens.format(cost.input)} in, ${tokens.format(cost.output)} out`}>
        <span>{costFloor}{tokens.format(cost.input + cost.output)}</span>
      </Tooltip>
    )])
    if (cost.cost > 0) cells.push(['Cost', `${costFloor}${money.format(cost.cost)}`])
  }
  // Stacked, the last cell fills its row so no seam-coloured hole is left.
  const rest = 3 - ((cells.length - 1) % 3)
  return (
    <dl className={cn('grid shrink-0 gap-px border-b border-seam bg-seam', wide ? 'h-16.5 auto-cols-fr grid-flow-col' : 'grid-cols-3')}>
      {cells.map(([label, value], i) => (
        <Stat key={label} label={label} style={!wide && i === cells.length - 1 ? { gridColumn: `span ${rest}` } : undefined}>
          {value}
        </Stat>
      ))}
    </dl>
  )
}

/** The track's place in a row or overlay, in pixels. */
export function trackBox(wide: boolean, track: number): React.CSSProperties {
  return { left: wide ? treeWidth : stackedInset, width: track }
}

export function TraceAxisHeader({
  marks,
  wide,
  track,
  zoomed,
  onWholeRun,
  onExpand,
}: {
  marks: AxisMark[]
  wide: boolean
  track: number
  zoomed: boolean
  onWholeRun: () => void
  onExpand: (open: boolean) => void
}) {
  return (
    <div className="relative h-7.25 shrink-0 border-b border-seam">
      <div aria-hidden className="absolute inset-y-0 overflow-hidden" style={trackBox(wide, track)}>
        {marks.map((mark) => (
          <span
            key={`${mark.kind}:${mark.a}`}
            className={cn('absolute top-1/2 -translate-y-1/2 text-ui-xs whitespace-nowrap tabular-nums', mark.kind === 'tick' ? 'pl-1.5 text-muted' : 'pl-2.5 text-icon-faint')}
            style={{ left: trackX(mark.a) }}
          >
            {mark.label}
          </span>
        ))}
      </div>
      <div className={cn('absolute inset-y-0 flex items-center gap-1 pr-2 pl-4', wide ? 'left-0 border-r border-seam bg-canvas' : 'right-0 bg-canvas')} style={wide ? { width: treeWidth } : undefined}>
        {wide && <span className="mr-auto text-ui-xs text-muted">Span</span>}
        {zoomed && (
          <Button variant="ghost" size="sm" hint="Escape" onClick={onWholeRun}>
            Whole session
          </Button>
        )}
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" label="Trace actions">
              <Ellipsis />
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            <MenuItem onSelect={() => onExpand(true)}>Expand all</MenuItem>
            <MenuItem onSelect={() => onExpand(false)}>Collapse all</MenuItem>
          </MenuContent>
        </Menu>
      </div>
    </div>
  )
}

/** One gridline layer behind every row, with each idle break as a band, history that is not loaded hatched, and the now line while live. */
export function TraceGrid({ marks, axis, wide, track, live }: { marks: AxisMark[]; axis: Axis; wide: boolean; track: number; live: boolean }) {
  return (
    <div aria-hidden className="pointer-events-none absolute inset-y-0 overflow-hidden" style={trackBox(wide, track)}>
      <span className="absolute inset-y-0 left-0 w-px bg-seam" />
      {marks.map((mark) => mark.kind === 'tick' && (
        <span key={mark.a} className="absolute inset-y-0 w-px bg-seam" style={{ left: trackX(mark.a) }} />
      ))}
      {axis.gaps.map((gap) => (
        <span
          key={gap.from}
          className={cn('absolute inset-y-0 border-x border-dashed border-seam bg-chrome', gap.unknown && 'trace-unknown')}
          style={{ left: trackX(axisAt(axis, gap.from)), width: `calc(${axis.breakMs} * var(--trace-scale) * 1px)` }}
        />
      ))}
      {live && <span className="absolute inset-y-0 w-px bg-state-working" style={{ left: trackX(null) }} />}
    </div>
  )
}

/** Now's dot on the header's lower edge, above the rows; its line is in the grid behind them. */
export function TraceNowMarker({ wide, track }: { wide: boolean; track: number }) {
  return (
    <div aria-hidden data-testid="trace-now" className="pointer-events-none absolute inset-y-0 z-10 overflow-hidden" style={trackBox(wide, track)}>
      <span className="absolute top-6.5 size-1.5 -translate-x-[2.5px] rounded-full bg-state-working ring-2 ring-canvas" style={{ left: trackX(null) }} />
    </div>
  )
}
