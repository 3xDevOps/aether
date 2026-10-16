import { useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import type { InsightsBucket, InsightsResult } from '@/lib/types'
import { cn } from '@/lib/utils'
import { bucketSelection, dayLabel, plural } from '@/routes/insights/model'

const segments = [
  { key: 'done', label: 'Done', fill: 'fill-state-done', of: (b: InsightsBucket) => b.completed + b.merged },
  { key: 'failed', label: 'Failed', fill: 'fill-state-failed', of: (b: InsightsBucket) => b.failed + b.interrupted },
  { key: 'abandoned', label: 'Abandoned', fill: 'fill-state-paused', of: (b: InsightsBucket) => b.abandoned },
] as const

const launchedMark = 'fill-chrome stroke-icon-faint'
const slot = 10
const height = 100

function ended(bucket: InsightsBucket): number {
  return segments.reduce((sum, segment) => sum + segment.of(bucket), 0)
}

function bucketWords(bucket: InsightsBucket, unit: InsightsResult['bucket']): string {
  const counts = segments.map((segment) => `${segment.of(bucket)} ${segment.label.toLowerCase()}`).join(', ')
  return `${bucketSelection(bucket.start, unit).label}: ${plural(bucket.launched, 'run')} launched, ${counts}`
}

export function Swatch({ fill }: { fill: string }) {
  return (
    <svg viewBox="0 0 10 10" aria-hidden className="size-2.5 shrink-0">
      <rect x="0.5" y="0.5" width="9" height="9" rx="2" className={fill} />
    </svg>
  )
}

export function ActivityChart({
  series,
  bucket,
  selected,
  onSelect,
}: {
  series: InsightsBucket[]
  bucket: InsightsResult['bucket']
  selected: string | null
  onSelect: (start: string | null) => void
}) {
  const [focus, setFocus] = useState(() => Math.max(0, series.length - 1))
  const refs = useRef<(SVGGElement | null)[]>([])
  const scroller = useRef<HTMLDivElement>(null)
  const minSlot = useMediaQuery(coarsePointer) ? 44 : 6
  useLayoutEffect(() => {
    const node = scroller.current
    if (node) node.scrollLeft = node.scrollWidth
  }, [])
  const max = Math.max(1, ...series.map((b) => Math.max(b.launched, ended(b))))
  const scale = (n: number) => (n / max) * (height - 2)
  const launched = series.reduce((sum, b) => sum + b.launched, 0)
  const total = series.reduce((sum, b) => sum + ended(b), 0)
  const unit = bucket === 'day' ? 'day' : 'week'

  const move = (index: number) => {
    const next = Math.min(series.length - 1, Math.max(0, index))
    setFocus(next)
    refs.current[next]?.focus()
  }
  const onKeyDown = (event: KeyboardEvent<SVGGElement>, index: number) => {
    const keys: Record<string, number> = { ArrowLeft: index - 1, ArrowRight: index + 1, Home: 0, End: series.length - 1 }
    if (event.key in keys) {
      event.preventDefault()
      move(keys[event.key])
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      onSelect(series[index].start === selected ? null : series[index].start)
    }
  }

  if (series.length === 0) return null
  const middle = series[Math.floor(series.length / 2)]
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="flex min-w-0 items-stretch gap-2">
        <div aria-hidden className="flex h-36 w-6 shrink-0 flex-col justify-between text-right text-ui-xs tabular-nums text-muted">
          <span>{max}</span>
          <span>0</span>
        </div>
        <div ref={scroller} className="min-w-0 flex-1 overflow-x-auto">
          <div className="flex flex-col gap-2" style={{ minWidth: series.length * minSlot }}>
            <svg
              role="group"
              aria-label={`Runs per ${unit}: ${launched} launched, ${total} ended. Choose a ${unit} to list the runs that finished in it.`}
              viewBox={`0 0 ${series.length * slot} ${height}`}
              preserveAspectRatio="none"
              className="h-36 w-full overflow-visible"
            >
              <line x1="0" x2={series.length * slot} y1={height} y2={height} vectorEffect="non-scaling-stroke" className="stroke-seam" />
              {series.map((b, i) => {
                const x = i * slot
                let top = height
                const isSelected = b.start === selected
                return (
                  <g
                    key={b.start}
                    ref={(node) => {
                      refs.current[i] = node
                    }}
                    role="button"
                    tabIndex={i === focus ? 0 : -1}
                    aria-pressed={isSelected}
                    aria-label={bucketWords(b, bucket)}
                    onClick={() => {
                      setFocus(i)
                      onSelect(isSelected ? null : b.start)
                    }}
                    onKeyDown={(event) => onKeyDown(event, i)}
                    className="group/bar cursor-pointer outline-none"
                  >
                    <title>{bucketWords(b, bucket)}</title>
                    <rect
                      x={x}
                      y={0}
                      width={slot}
                      height={height}
                      vectorEffect="non-scaling-stroke"
                      className={cn(
                        'stroke-2 group-focus-visible/bar:stroke-accent',
                        isSelected ? 'fill-selection' : 'fill-transparent group-hover/bar:fill-hover',
                      )}
                    />
                    {b.launched > 0 && (
                      <rect x={x + 1} y={height - scale(b.launched)} width={3.5} height={scale(b.launched)} vectorEffect="non-scaling-stroke" className={launchedMark} />
                    )}
                    {segments.map((segment) => {
                      const h = scale(segment.of(b))
                      top -= h
                      return h > 0 && <rect key={segment.key} x={x + 5.5} y={top} width={3.5} height={h} className={segment.fill} />
                    })}
                  </g>
                )
              })}
            </svg>
            <div aria-hidden className="flex justify-between gap-2 text-ui-xs tabular-nums text-muted">
              <span>{dayLabel(series[0].start)}</span>
              {series.length > 2 && <span>{dayLabel(middle.start)}</span>}
              <span>{dayLabel(series[series.length - 1].start)}</span>
            </div>
          </div>
        </div>
      </div>
      <ul aria-label="Legend" className="ml-8 flex flex-wrap gap-x-4 gap-y-1 text-ui-sm text-muted">
        <li className="flex items-center gap-1.5"><Swatch fill={launchedMark} />Launched (outlined, left)</li>
        {segments.map((segment) => (
          <li key={segment.key} className="flex items-center gap-1.5"><Swatch fill={segment.fill} />{segment.label}</li>
        ))}
      </ul>
    </div>
  )
}
