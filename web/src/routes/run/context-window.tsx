import { useRef, useState } from 'react'
import type * as React from 'react'
import { FoldVertical } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { ProgressBar } from '@/components/ui/progress-bar'
import { TextSwap } from '@/components/ui/text-swap'
import type { SessionUsage } from '@/lib/session-types'
import { useNumberTween } from '@/lib/use-number-tween'
import { cn } from '@/lib/utils'
import { cellCount, contextSegments, formatCost, formatShare, percentUsed, previousTurnUsage, type Segment, type SegmentKey, usageFormat, warnPercent } from '@/routes/run/context-usage'
import { useStore } from '@/store'

const columns = 24

export interface ContextCompact {
  onCompact: () => void
  /** Why Compact cannot run now, shown on hover and focus. */
  blocked?: string
}

function cellTone(key: SegmentKey, warn: boolean): string {
  if (key === 'latest') return 'bg-accent'
  if (key === 'free') return 'bg-seam'
  return warn ? 'bg-state-needs-you' : 'bg-icon-faint'
}

function DotGrid({ segments, warn, focus, onFocus }: {
  segments: Segment[]
  warn: boolean
  focus: SegmentKey | null
  onFocus: (key: SegmentKey | null) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const cells = segments.flatMap((segment) => Array<SegmentKey>(segment.cells).fill(segment.key))
  // The hit area is the whole pitch, so the gap between two cells still counts as one of them.
  const point = (event: React.PointerEvent) => {
    if (event.pointerType === 'touch') return
    const box = ref.current!.getBoundingClientRect()
    const column = Math.floor(((event.clientX - box.left) / box.width) * columns)
    const row = Math.floor(((event.clientY - box.top) / box.height) * (cellCount / columns))
    onFocus(cells[row * columns + column] ?? null)
  }
  return (
    <div
      ref={ref}
      aria-hidden="true"
      data-slot="dot-grid"
      className="mt-3 grid grid-cols-[repeat(24,minmax(0,1fr))] gap-0.5"
      onPointerMove={point}
      onPointerLeave={() => onFocus(null)}
    >
      {cells.map((key, index) => (
        <span
          key={index}
          // Only the recolour staggers; the hover dim moves every cell together.
          style={{ transitionDelay: `calc(var(--stagger-cell) * ${index}), 0ms` }}
          className={cn(
            'aspect-square rounded-xs transition-[background-color,opacity] duration-glide ease-glide motion-reduce:transition-none',
            cellTone(key, warn),
            focus && focus !== key && 'opacity-25',
          )}
        />
      ))}
    </div>
  )
}

function LegendRow({ segment, warn, focus, onFocus }: {
  segment: Segment
  warn: boolean
  focus: SegmentKey | null
  onFocus: (key: SegmentKey | null) => void
}) {
  const touched = useRef(false)
  const interactive = (event: React.PointerEvent) => event.pointerType !== 'touch'
  return (
    <li
      tabIndex={0}
      onPointerDown={(event) => { touched.current = !interactive(event) }}
      onKeyDown={() => { touched.current = false }}
      onFocus={() => !touched.current && onFocus(segment.key)}
      onBlur={() => { touched.current = false; onFocus(null) }}
      onPointerEnter={(event) => interactive(event) && onFocus(segment.key)}
      onPointerLeave={(event) => interactive(event) && onFocus(null)}
      className={cn('-mx-2 flex h-8 items-center gap-2 rounded-control px-2 coarse:h-11 outline-none focus-visible:outline-2 focus-visible:outline-accent', focus === segment.key && 'bg-hover-chrome')}
    >
      <span aria-hidden="true" className={cn('size-2 shrink-0 rounded-xs', cellTone(segment.key, warn))} />
      <span className={cn('min-w-0 flex-1 truncate text-ui', focus && focus !== segment.key ? 'text-muted' : 'text-text')}>{segment.label}</span>
      <span className="text-ui-sm text-muted tabular-nums">{usageFormat.format(segment.tokens)}</span>
    </li>
  )
}

function ContextPanel({ runID, usage, model, harness, compact, onCompact }: {
  runID: string
  usage: SessionUsage
  model?: string
  harness: string
  compact?: ContextCompact
  onCompact: () => void
}) {
  const previous = useStore((s) => previousTurnUsage(s.acpSessions[runID]?.turns ?? []))
  const segments = contextSegments(usage, previous)
  const [hovered, setFocus] = useState<SegmentKey | null>(null)
  const active = segments.find((segment) => segment.key === hovered)
  const focus = active?.key ?? null
  // One category has nothing to single out.
  const setHover = segments.length > 1 ? setFocus : () => {}
  const percent = percentUsed(usage)
  const warn = percent >= warnPercent
  const shown = useNumberTween(active?.tokens ?? usage.used)
  const left = usage.size - usage.used

  return (
    <>
      <div className="px-4 pb-2 pt-3.5">
        <header className="flex h-5 items-center justify-between gap-2">
          <h2 className="shrink-0 text-ui font-medium text-text">Context window</h2>
          {model && (
            <Badge className="min-w-0 max-w-1/2 text-ui-xs">
              <AgentGlyph agent={harness} />
              <span className="truncate">{model}</span>
            </Badge>
          )}
        </header>
        <div
          role="meter"
          aria-label="Context window used"
          aria-valuemin={0}
          aria-valuemax={usage.size}
          aria-valuenow={Math.min(usage.used, usage.size)}
          aria-valuetext={`${usageFormat.format(usage.used)} of ${usageFormat.format(usage.size)} tokens used, ${percent}%`}
          className="mt-2 flex h-8 items-center justify-between gap-2"
        >
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="text-title text-text tabular-nums">{usageFormat.format(Math.round(shown))}</span>
            <TextSwap swapKey={focus ?? 'all'} mode="crossfade" className="text-ui-sm whitespace-nowrap text-muted">
              {active ? active.label : `of ${usageFormat.format(usage.size)} tokens`}
            </TextSwap>
          </div>
          <Badge tone={warn ? 'needs-you' : 'neutral'} className="min-w-10 justify-center">{percent}%</Badge>
        </div>
        <DotGrid segments={segments} warn={warn} focus={focus} onFocus={setHover} />
        <ul className="mt-2.5">
          {segments.map((segment) => <LegendRow key={segment.key} segment={segment} warn={warn} focus={focus} onFocus={setHover} />)}
          {usage.cost !== undefined && (
            <li className="-mx-2 flex h-8 items-center gap-2 px-2 coarse:h-11">
              <span aria-hidden="true" className="size-2 shrink-0" />
              <span className="min-w-0 flex-1 truncate text-ui text-muted">Session cost</span>
              <span className="text-ui-sm text-muted tabular-nums">{formatCost(usage.cost, usage.currency)}</span>
            </li>
          )}
        </ul>
      </div>
      <div className="flex h-11 items-center justify-between gap-2 border-t border-seam bg-chrome pl-4 pr-2 text-ui-sm tabular-nums">
        <TextSwap swapKey={focus ?? (left >= 0 ? 'left' : 'over')} mode="crossfade" className="min-w-0 grid-cols-[minmax(0,1fr)] overflow-hidden whitespace-nowrap" layerClassName="truncate">
          {active ? (
            <>
              <span className="font-medium text-text">{active.label}</span>
              <span className="text-muted"> · {formatShare(active.tokens, usage.size)}% of the window</span>
            </>
          ) : (
            <span className={warn ? 'text-state-needs-you' : 'text-muted'}>
              {left >= 0 ? `${usageFormat.format(left)} left` : `${usageFormat.format(-left)} over the window`}
            </span>
          )}
        </TextSwap>
        {compact && (
          <Button
            variant="ghost"
            size="sm"
            aria-disabled={compact.blocked ? true : undefined}
            hint={compact.blocked ?? 'Puts /compact in the message box, ready to send'}
            onClick={() => {
              if (!compact.blocked) onCompact()
            }}
          >
            <FoldVertical aria-hidden="true" />
            Compact
          </Button>
        )}
      </div>
    </>
  )
}

export function ContextWindow({ runID, harness, usage, model, compact }: {
  runID: string
  harness: string
  usage: SessionUsage
  model?: string
  compact?: ContextCompact
}) {
  const [open, setOpen] = useState(false)
  const compacted = useRef(false)
  const panel = useRef<HTMLDivElement>(null)
  const percent = percentUsed(usage)
  const used = usageFormat.format(usage.used)
  const size = usageFormat.format(usage.size)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" className="tabular-nums" aria-label={`Context window, ${percent}% used, ${used} of ${size} tokens`} hint={`${used} of ${size} tokens used`}>
          <ProgressBar aria-hidden="true" value={percent} className="w-4 bg-seam" fillClassName={percent >= warnPercent ? 'bg-state-needs-you' : 'bg-icon-faint'} />
          {percent}%
        </Button>
      </PopoverTrigger>
      <PopoverContent
        ref={panel}
        side="top"
        align="end"
        aria-label="Context window"
        className="w-[min(320px,calc(100vw-16px))] p-0"
        // Opening must not land on the first legend row, which would single out its category.
        onOpenAutoFocus={(event) => {
          event.preventDefault()
          panel.current?.focus()
        }}
        // Compact hands focus to the message box; the trigger must not take it back.
        onCloseAutoFocus={(event) => {
          if (!compacted.current) return
          compacted.current = false
          event.preventDefault()
        }}
      >
        <ContextPanel
          runID={runID}
          usage={usage}
          model={model}
          harness={harness}
          compact={compact}
          onCompact={() => {
            compacted.current = true
            setOpen(false)
            compact?.onCompact()
          }}
        />
      </PopoverContent>
    </Popover>
  )
}
