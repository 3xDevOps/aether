import { memo, type CSSProperties, type ReactNode } from 'react'
import { ChevronRight, Waypoints } from '@/components/icons'
import { modeLabel } from '@/components/launch/modes'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Avatar } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { StatusDot, toneClasses, type Tone } from '@/components/ui/status-dot'
import { compactAge } from '@/lib/format'
import { stateLabel, stateTone, stateWords } from '@/lib/status'
import { cn, focusRingInset } from '@/lib/utils'
import { elapsed } from '@/routes/missions/tree'
import { xOf, type TimeWindow } from '@/routes/board/lanes/axis'
import type { Lane, LaneMetrics, RunLane } from '@/routes/board/lanes/model'
import { segmentTone, type Segment, type Stretch } from '@/routes/board/lanes/segments'
import { agentTone } from '@/routes/run/subagent-cards'
import type { PresentationState } from '@/lib/status'

export const trackGutter = 28
const indent = 16

export interface LaneGeometry {
  window: TimeWindow
  width: number
  labelWidth: number
  compact: boolean
  metrics: LaneMetrics
}

/** A state's colour at `--lane-tint`, which keeps `text-text` at 4.5:1 or more; Starting and unloaded history stay hollow. */
export function barFill(tone: ReturnType<typeof segmentTone>): string {
  if (tone === 'starting') return 'border border-dashed border-seam'
  if (tone === 'unloaded') return 'border border-dotted border-icon-faint'
  return cn(toneClasses(tone).text, 'bg-current/(--lane-tint)')
}

const fade: CSSProperties = {
  maskImage: 'linear-gradient(to right, transparent, currentColor 32px)',
  WebkitMaskImage: 'linear-gradient(to right, transparent, currentColor 32px)',
}

/** The visible part of [start, end] in track pixels, at least 2px wide; undefined when it is out of view. */
function span(start: number, end: number, g: LaneGeometry): { left: number; width: number } | undefined {
  const a = Math.max(0, xOf(start, g.window, g.width))
  const b = Math.min(g.width, xOf(end, g.window, g.width))
  if (b < 0 || a > g.width) return undefined
  const width = Math.max(2, b - a)
  return { left: Math.min(a, g.width - width), width }
}

function Segments({ segments, outcome, g, height, className, children }: {
  segments: Segment[]
  outcome?: PresentationState
  g: LaneGeometry
  height: number
  className?: string
  children?: ReactNode
}) {
  if (segments.length === 0) return null
  const box = span(segments[0].start, segments.at(-1)!.end, g)
  if (!box) return null
  // Neighbours that share a fill draw as one span; the popover still reads the exact states from the lane.
  const parts: { start: number; end: number; fill: string }[] = []
  for (const segment of segments) {
    const tone = barFill(segmentTone(segment.state, outcome))
    const last = parts.at(-1)
    if (last?.fill === tone) last.end = segment.end
    else parts.push({ start: segment.start, end: segment.end, fill: tone })
  }
  return (
    <div
      className={cn('absolute top-1/2 -translate-y-1/2 overflow-hidden rounded-control', className)}
      style={{ left: box.left, width: box.width, height, ...(segments[0].start < g.window.start ? fade : undefined) }}
    >
      {parts.map((segment, i) => {
        const part = span(segment.start, segment.end, g)
        if (!part) return null
        const last = i === parts.length - 1
        return (
          <span
            key={segment.start}
            className={cn('absolute inset-y-0', segment.fill)}
            style={{ left: part.left - box.left, width: Math.max(1, part.width - (last ? 0 : 1)) }}
          />
        )
      })}
      {children}
    </div>
  )
}

function Strip({ strip, g, height }: { strip: Stretch[]; g: LaneGeometry; height: number }) {
  // Tones that share a colour draw as one pill, without a notch where they meet.
  const parts: { start: number; end: number; text: string }[] = []
  for (const stretch of strip) {
    const text = toneClasses(stretch.tone).text
    const last = parts.at(-1)
    if (last?.text === text && last.end >= stretch.start) last.end = stretch.end
    else parts.push({ start: stretch.start, end: stretch.end, text })
  }
  return parts.map((stretch) => {
    const part = span(stretch.start, stretch.end, g)
    if (!part) return null
    return (
      <span
        key={stretch.start}
        className={cn('absolute top-1/2 -translate-y-1/2 rounded-full bg-current', stretch.text)}
        style={{ left: part.left, width: part.width, height }}
      />
    )
  })
}

function barText(lane: RunLane): string {
  const { card } = lane
  if (lane.outcome) return stateLabel[card.state]
  if (card.group === 'needs-you') return card.reason
  const words = stateWords(card)
  return [words.label, words.detail].filter(Boolean).join(' · ')
}

function RunBar({ lane, g, now }: { lane: RunLane; g: LaneGeometry; now: number }) {
  const { card } = lane
  const live = !lane.outcome
  const working = live && card.state === 'working'
  const needs = live && card.group === 'needs-you'
  const box = span(lane.start, lane.end, g)
  const after = box && lane.outcome ? box.left + box.width + 6 : undefined
  return (
    <>
      <Segments segments={lane.segments} outcome={lane.outcome} g={g} height={g.metrics.bar} className={working ? 'border-r-2 border-state-working' : undefined}>
        <span className="relative flex h-full min-w-0 items-center gap-1.5 px-2 text-ui-sm font-medium whitespace-nowrap text-text">
          <StatusDot tone={stateTone[card.state]} />
          <span className={cn('min-w-0 overflow-hidden', working && 'live-shimmer')}>{g.compact ? lane.title : barText(lane)}</span>
        </span>
        {needs && box && box.width > 200 && (
          <span aria-hidden className="absolute top-1/2 right-1 flex h-4.5 -translate-y-1/2 items-center gap-1 rounded-chip bg-raised px-1.5 text-ui-xs font-medium text-state-needs-you shadow-button">
            <StatusDot tone="needs-you" pulse />
            Needs you
          </span>
        )}
      </Segments>
      {after !== undefined && after + 64 < g.width && (
        <span aria-hidden className="absolute top-1/2 -translate-y-1/2 text-ui-xs whitespace-nowrap text-muted tabular-nums" style={{ left: after }}>
          took {elapsed(card.run.created_at, Math.min(lane.end, now))}
        </span>
      )}
    </>
  )
}

function Chevron({ open }: { open: boolean }) {
  return <ChevronRight aria-hidden data-lane-chevron className={cn('size-3.5 shrink-0 text-muted transition-transform duration-200 motion-reduce:transition-none', open && 'rotate-90')} />
}

function Elbow({ x, last }: { x: number; last: boolean }) {
  return (
    <span aria-hidden className="pointer-events-none absolute inset-y-0" style={{ left: x }}>
      <span className="absolute top-0 left-0 h-1/2 w-2 border-b border-l border-icon-faint" />
      {!last && <span className="absolute top-1/2 bottom-0 left-0 border-l border-icon-faint" />}
    </span>
  )
}

function RunLabel({ lane, agentName, compact, now }: { lane: RunLane; agentName?: string; compact: boolean; now: number }) {
  const { card } = lane
  const state = card.group === 'needs-you' && !card.needsYou ? 'needs-you' : card.state
  const owner = (
    <span
      aria-hidden
      data-slot="owner-mark"
      style={{ backgroundColor: card.owner?.color }}
      className="h-1.75 w-1.5 shrink-0 bg-icon-faint [clip-path:polygon(0_0,100%_50%,0_100%)]"
    />
  )
  if (compact) {
    return (
      <span className="flex items-center gap-1">
        <StatusDot tone={stateTone[state]} pulse={state === 'working'} />
        <AgentGlyph agent={card.run.harness} />
      </span>
    )
  }
  return (
    <span className="flex min-w-0 flex-1 flex-col justify-center">
      <span className="flex min-w-0 items-center gap-1.5">
        <StatusDot tone={stateTone[state]} />
        {owner}
        <span className={cn('min-w-0 truncate text-ui leading-4 font-medium', lane.outcome && 'text-muted')}>{lane.title}</span>
        {card.group === 'needs-you' && <Badge tone="needs-you" className="h-4 text-ui-xs">Needs you</Badge>}
      </span>
      <span className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
        <AgentGlyph agent={card.run.harness} />
        <span className="min-w-0 truncate">{lane.handle ?? `${agentName ?? card.run.harness} · ${modeLabel(card.run.mode)}`}</span>
        {card.workspaceName && <span className="min-w-0 truncate">· {card.workspaceName}</span>}
        <span className="ml-auto shrink-0 tabular-nums">{compactAge(card.run.created_at, now)}</span>
      </span>
    </span>
  )
}

export interface LaneRowProps {
  lane: Lane
  top: number
  height: number
  g: LaneGeometry
  now: number
  tree: { level: number; posinset: number; setsize: number; expanded?: boolean }
  /** The lane's accessible name, apart from `tree` because it follows the clock. */
  name: string
  active: boolean
  selected: boolean
  agentName?: string
  widen?: string
  onPick: (lane: Lane, x: number | undefined, chevron: boolean) => void
  onWiden: () => void
  onRetry: (runID: string) => void
}

export const LaneRow = memo(function LaneRow({ lane, top, height, g, now, tree, name, active, selected, agentName, widen, onPick, onWiden, onRetry }: LaneRowProps) {
  const nested = lane.kind === 'agent' || lane.kind === 'note' || (lane.kind === 'run' && lane.level > 2)
  const pad = g.compact ? (nested ? 12 : 8) : 12 + Math.max(0, lane.level - 2) * indent
  let label: ReactNode
  let track: ReactNode = null
  switch (lane.kind) {
    case 'section': {
      const before = lane.before > 0 && (
        <>
          <span className="min-w-0 truncate">{g.compact && '· '}{lane.before} ended before this window</span>
          {widen && <Button variant="link" size="sm" tabIndex={-1} className="shrink-0" onClick={(event) => { event.stopPropagation(); onWiden() }}>Show {widen}</Button>}
        </>
      )
      label = (
        <span className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
          <Chevron open={lane.open} />
          {lane.member && <Avatar name={lane.member.display_name} color={lane.member.color} />}
          <span className="min-w-0 truncate">{lane.label}</span>
          <span className="tabular-nums">{lane.count}</span>
          {g.compact && before}
        </span>
      )
      if (!g.compact && before) track = <span className="flex h-full items-center gap-2 text-ui-sm text-muted">{before}</span>
      break
    }
    case 'summary':
      label = <span className="min-w-0 truncate text-ui-sm text-muted">{lane.label}</span>
      track = <Strip strip={lane.strip} g={g} height={8} />
      break
    case 'swarm':
      label = (
        <span className="flex min-w-0 flex-1 items-center gap-1.5">
          <Chevron open={lane.open} />
          <Waypoints aria-hidden className="size-3.5 shrink-0 text-muted" />
          <span className="flex min-w-0 flex-col">
            <span className="truncate text-ui leading-4 font-medium">{lane.title}</span>
            <span className="truncate text-ui-sm text-muted">{lane.detail}</span>
          </span>
        </span>
      )
      if (!lane.open) {
        track = (
          <>
            <span className="absolute inset-x-0 top-1.5 h-4">
              <Segments segments={lane.segments} outcome={lane.outcome} g={g} height={16} />
            </span>
            <span className="absolute inset-x-0 bottom-1.5 h-1.5">
              <Strip strip={lane.strip} g={g} height={6} />
            </span>
          </>
        )
      }
      break
    case 'run':
      label = (
        <>
          {lane.agentsOpen && <Chevron open />}
          <RunLabel lane={lane} agentName={agentName} compact={g.compact} now={now} />
        </>
      )
      track = <RunBar lane={lane} g={g} now={now} />
      break
    case 'agent': {
      const tone: Tone = agentTone(lane.state)
      label = (
        <span className="flex min-w-0 items-center gap-1.5 text-ui-sm">
          <StatusDot tone={tone} pulse={lane.state === 'running'} />
          {!g.compact && <span className="min-w-0 truncate">{lane.label}</span>}
        </span>
      )
      const part = span(lane.start ?? g.window.start, lane.end, g)
      track = part && (
        <span
          className={cn('absolute top-1/2 h-3.5 -translate-y-1/2 rounded-chip', barFill(lane.start === undefined ? 'unloaded' : tone))}
          style={{ left: part.left, width: part.width }}
        />
      )
      break
    }
    case 'note':
      label = (
        <span className="flex min-w-0 items-center gap-2 text-ui-sm text-muted">
          <span className="min-w-0 truncate" title={lane.text}>{lane.text}</span>
          {lane.retry && <Button variant="link" size="sm" tabIndex={-1} onClick={(event) => { event.stopPropagation(); onRetry(lane.runID) }}>Retry</Button>}
        </span>
      )
  }
  const header = g.compact && (lane.kind === 'section' || lane.kind === 'swarm')
  return (
    <div
      role="treeitem"
      data-key={lane.key}
      data-glide-key={lane.key}
      tabIndex={active ? 0 : -1}
      aria-level={tree.level}
      aria-posinset={tree.posinset}
      aria-setsize={tree.setsize}
      aria-expanded={tree.expanded}
      aria-selected={selected}
      aria-label={name}
      className={cn(
        focusRingInset,
        'absolute inset-x-0 flex cursor-default items-stretch outline-none',
        lane.kind === 'section' && 'border-t border-seam first:border-t-0',
      )}
      style={{ top, height }}
      onClick={(event) => {
        const target = event.target as Element
        const track = target.closest('[data-lane-track]')
        onPick(lane, track ? event.clientX - track.getBoundingClientRect().left : undefined, Boolean(target.closest('[data-lane-chevron]')))
      }}
    >
      <div className="relative flex min-w-0 shrink-0 items-center gap-1 pr-2" style={{ width: header ? g.labelWidth + g.width : g.labelWidth, paddingLeft: pad }}>
        {nested && <Elbow x={pad - 10} last={tree.posinset === tree.setsize} />}
        {label}
      </div>
      {!header && (
        <div data-lane-track className="relative shrink-0" style={{ width: g.width }}>
          {track}
        </div>
      )}
    </div>
  )
})
