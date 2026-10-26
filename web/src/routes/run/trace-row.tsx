import { createContext, memo, useContext } from 'react'
import type * as React from 'react'
import {
  Bot,
  ChevronRight,
  Hourglass,
  MessageSquare,
  Play,
  TriangleAlert,
  Users,
  Wrench,
  type LucideIcon,
} from '@/components/icons'
import { CountBadge } from '@/components/ui/count-badge'
import { Spinner } from '@/components/ui/spinner'
import type { Tone } from '@/components/ui/status-dot'
import { toolIcon } from '@/components/ui/timeline'
import { formatDuration } from '@/lib/format'
import { cn, focusRingInset } from '@/lib/utils'
import { quietDuration } from '@/store/session-rows'
import type { Span, SpanKind, SpanStatus } from '@/store/trace'
import { axisAt, type Axis } from '@/store/trace-axis'
import { barSegments, type SegmentTone, type TraceRow } from '@/store/trace-rows'

/** The live clock, as server time and its place on the axis; only running rows read it. */
export const TraceClock = createContext({ now: 0, nowA: 0 })

export interface TraceGeometry {
  axis: Axis
  /** Axis time at the track's left edge. */
  origin: number
  /** Pixels per axis millisecond. */
  scale: number
  track: number
  wide: boolean
}

export const kindLabel: Record<SpanKind, string> = {
  run: 'Run',
  turn: 'Turn',
  agent: 'Subagent',
  tool: 'Tool call',
  wait: 'Waiting for you',
  notice: 'Error',
  workers: 'Workers',
  worker: 'Worker',
}

export const statusLabel: Record<SpanStatus, string> = {
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  stopped: 'Stopped',
  waiting: 'Needs you',
}

export const statusTone: Record<SpanStatus, Tone> = {
  running: 'working',
  done: 'done',
  failed: 'failed',
  stopped: 'paused',
  waiting: 'needs-you',
}

const kindIcon: Partial<Record<SpanKind, LucideIcon>> = {
  run: Play,
  turn: MessageSquare,
  agent: Bot,
  wait: Hourglass,
  notice: TriangleAlert,
  workers: Users,
  worker: Bot,
}

export function spanIcon(span: Span): LucideIcon {
  return kindIcon[span.kind] ?? toolIcon[span.tool?.toLowerCase() ?? ''] ?? Wrench
}

/** The span's length so far: "6.9s", "instant" under 50ms, or nothing for a marker. */
export function spanDuration(span: Span, now: number): string {
  if (span.kind === 'notice') return ''
  const ms = (span.end ?? Math.max(now, span.start)) - span.start
  return span.end !== null && ms < quietDuration ? 'instant' : formatDuration(ms)
}

const leafBar: Record<SpanStatus, string> = {
  running: 'trace-live border-state-working bg-state-working-soft',
  done: 'border-accent bg-accent-soft',
  failed: 'border-state-failed bg-state-failed-soft',
  stopped: 'border-state-paused bg-state-paused-soft',
  waiting: 'border-state-needs-you bg-state-needs-you-soft',
}

const segmentClass: Record<SegmentTone, string> = {
  model: 'bg-icon-faint/50',
  tool: 'bg-accent/50',
  overlap: 'bg-accent/75',
  dense: 'bg-accent',
  failed: 'bg-state-failed',
  wait: 'bg-state-needs-you',
}

function barClass(span: Span): string {
  if (span.kind === 'wait') return leafBar.waiting
  if (span.kind === 'run') return 'border-seam bg-chrome'
  if (span.children.length === 0) return leafBar[span.status]
  return span.status === 'failed' ? leafBar.failed : 'border-seam bg-chrome'
}

/** Where an axis time sits in the track, as CSS: zoom and scale steps glide through the list's variables. */
export function trackX(a: number | null): string {
  return `calc((${a ?? 'var(--trace-now)'} - var(--trace-origin)) * var(--trace-scale) * 1px)`
}

function placed(axis: Axis, from: number, to: number | null): React.CSSProperties {
  const a0 = axisAt(axis, from)
  const a1 = to === null ? 'var(--trace-now)' : axisAt(axis, to)
  // An instant at the track's end keeps its 2px mark inside the clipped track.
  return { left: `min(${trackX(a0)}, calc(100% - 2px))`, width: `max(2px, calc((${a1} - ${a0}) * var(--trace-scale) * 1px))` }
}

function Bar({ span, axis, wide }: { span: Span; axis: Axis; wide: boolean }) {
  const parent = span.children.length > 0
  return (
    <>
      <span aria-hidden data-bar className={cn('absolute inset-y-0 rounded-[3px] border', barClass(span))} style={placed(axis, span.start, span.end)} />
      {span.lane?.map((segment, i) => (
        <span
          key={i}
          aria-hidden
          className={cn('absolute inset-y-px rounded-[2px]', laneClass[segment.tone] ?? 'bg-hover-chrome')}
          style={placed(axis, segment.from, segment.to)}
        />
      ))}
      {parent && span.kind !== 'run' && barSegments(span).map((segment, i) => (
        <span
          key={i}
          aria-hidden
          className={cn('absolute top-1/2 -translate-y-1/2 rounded-[2px]', wide ? 'h-1.5' : 'h-1', segmentClass[segment.tone])}
          style={placed(axis, segment.from, segment.to)}
        />
      ))}
    </>
  )
}

const laneClass: Partial<Record<Tone, string>> = {
  working: 'bg-state-working-soft',
  waiting: 'bg-hover-chrome',
  ended: 'bg-hover-chrome',
  'needs-you': 'bg-state-needs-you-soft',
  review: 'bg-accent-soft',
  failed: 'bg-state-failed-soft',
  interrupted: 'bg-state-failed-soft',
  done: 'bg-state-done-soft',
  paused: 'bg-state-paused-soft',
}

const labelGap = 6
const charWidth = 7

/** The duration after the bar, or inside its end when it would run past the track. */
function EndLabel({ span, geometry, slowest, end, text }: { span: Span; geometry: TraceGeometry; slowest: boolean; end: number; text: string }) {
  const { origin, scale, track } = geometry
  const width = (text.length + 2) * charWidth + (slowest ? 60 : 0)
  const inside = (end - origin) * scale + labelGap + width > track
  return (
    <span
      className={cn(
        'absolute top-1/2 flex -translate-y-1/2 items-center gap-1.5 text-ui-xs whitespace-nowrap tabular-nums',
        span.status === 'failed' ? 'text-state-failed' : 'text-muted group-data-selected/row:text-text',
        inside && 'px-1',
      )}
      style={{ left: `calc(${trackX(span.end === null ? null : end)} + ${labelGap}px)`, translate: inside ? `calc(-100% - ${2 * labelGap}px) -50%` : undefined }}
    >
      {text}
      {slowest && <span className="rounded-chip bg-hover-chrome px-1 text-muted">Slowest</span>}
    </span>
  )
}

function LiveEndLabel({ span, geometry }: { span: Span; geometry: TraceGeometry }) {
  const { now, nowA } = useContext(TraceClock)
  return <EndLabel span={span} geometry={geometry} slowest={false} end={nowA} text={spanDuration(span, now)} />
}

function Duration({ span }: { span: Span }) {
  const { now } = useContext(TraceClock)
  return <>{spanDuration(span, now)}</>
}

const indent = 14
const maxIndentLevel = 6

function Trailing({ row }: { row: TraceRow }) {
  const { span } = row
  if (row.expanded === false && span.size > 0) return <CountBadge aria-hidden>+{span.size}</CountBadge>
  if (span.status === 'waiting') return <Hourglass aria-hidden className="size-3.5 text-state-needs-you" />
  if (span.status === 'failed') return <TriangleAlert aria-hidden className="size-3.5 text-state-failed" />
  if (span.status === 'running' && span.children.length === 0) return <Spinner className="size-3 text-state-working" />
  return null
}

interface RowProps {
  row: TraceRow
  geometry: TraceGeometry
  active: boolean
  selected: boolean
  slowest: boolean
  fresh: boolean
  onActivate: (row: TraceRow) => void
  onToggle: (row: TraceRow) => void
  register: (key: string, element: HTMLElement | null) => void
}

function RowLabel({ row, wide }: { row: TraceRow; wide: boolean }) {
  const { span } = row
  const Icon = spanIcon(span)
  const parent = span.children.length > 0
  return (
    <>
      {row.expanded === undefined ? (
        <span aria-hidden className="w-4 shrink-0" />
      ) : (
        <span aria-hidden data-chevron className="flex size-4 shrink-0 items-center justify-center text-icon-faint hover:text-text">
          <ChevronRight className={cn('size-3.5 transition-transform duration-200 motion-reduce:transition-none', row.expanded && 'rotate-90')} />
        </span>
      )}
      <span aria-hidden className="flex size-5 shrink-0 items-center justify-center rounded-chip border border-seam bg-chrome text-muted">
        <Icon className="size-3" />
      </span>
      <span className={cn('min-w-0 truncate text-ui text-text', parent ? 'font-medium' : row.surfaced && 'font-medium')}>{span.label}</span>
      {wide && span.sub && <span className="max-w-[30%] shrink-0 truncate text-ui-sm text-muted group-data-selected/row:text-text">{span.sub}</span>}
    </>
  )
}

function sameRow(a: RowProps, b: RowProps): boolean {
  return a.row.key === b.row.key && a.row.span === b.row.span && a.row.expanded === b.row.expanded && a.row.level === b.row.level &&
    a.row.posinset === b.row.posinset && a.row.setsize === b.row.setsize && a.geometry === b.geometry && a.active === b.active &&
    a.selected === b.selected && a.slowest === b.slowest && a.fresh === b.fresh && a.onActivate === b.onActivate &&
    a.onToggle === b.onToggle && a.register === b.register
}

export const TraceRowItem = memo(function TraceRowItem({ row, geometry, active, selected, slowest, fresh, onActivate, onToggle, register }: RowProps) {
  const { span } = row
  const { wide, axis } = geometry
  const duration = span.end === null ? undefined : spanDuration(span, 0)
  const pad = (Math.min(row.level, maxIndentLevel) - 1) * indent + (wide ? 8 : 10)
  return (
    <div
      ref={(element) => register(row.key, element)}
      role="treeitem"
      aria-level={row.level}
      aria-posinset={row.posinset}
      aria-setsize={row.setsize}
      aria-expanded={row.expanded}
      aria-selected={selected}
      aria-label={[span.label, kindLabel[span.kind], statusLabel[span.status], duration, slowest && 'slowest'].filter(Boolean).join(', ')}
      tabIndex={active ? 0 : -1}
      data-key={row.key}
      data-selected={selected ? '' : undefined}
      onClick={(event) => {
        if (event.target instanceof Element && event.target.closest('[data-chevron]')) onToggle(row)
        else onActivate(row)
      }}
      className={cn(
        focusRingInset,
        'group/row relative cursor-default hover:bg-hover data-selected:bg-selection',
        wide ? 'flex h-7.5 coarse:h-11' : 'h-11.5 coarse:h-13',
        fresh && 'animate-entrance motion-reduce:animate-none',
      )}
    >
      <div className={cn('flex min-w-0 items-center gap-2', wide ? 'h-full w-64 shrink-0 pr-3' : 'h-7 pr-2.5')} style={{ paddingLeft: pad }}>
        <RowLabel row={row} wide={wide} />
        <span className="ml-auto flex shrink-0 items-center gap-1.5">
          <Trailing row={row} />
          {!wide && slowest && <span aria-hidden className="size-1.5 rounded-full bg-icon-faint" />}
          {!wide && span.kind !== 'notice' && (
            <span className={cn('text-ui-xs tabular-nums', span.status === 'failed' ? 'text-state-failed' : 'text-muted')}>
              {duration ?? <Duration span={span} />}
            </span>
          )}
        </span>
      </div>
      <div className={cn('absolute overflow-hidden', wide ? 'inset-y-0 left-64 right-4' : 'inset-x-2.5 bottom-2 h-2.5')}>
        <div className={cn('absolute inset-x-0', wide ? 'top-1/2 h-3.5 -translate-y-1/2' : 'inset-y-0')}>
          <Bar span={span} axis={axis} wide={wide} />
        </div>
        {span.laneError && (
          <span className="absolute inset-y-0 left-1.5 flex max-w-[60%] items-center text-ui-xs text-state-failed">
            <span className="truncate">Status history not read: {span.laneError}</span>
          </span>
        )}
        {wide && span.kind !== 'notice' && (duration === undefined
          ? <LiveEndLabel span={span} geometry={geometry} />
          : <EndLabel span={span} geometry={geometry} slowest={slowest} end={axisAt(axis, span.end!)} text={duration} />)}
      </div>
    </div>
  )
}, sameRow)
