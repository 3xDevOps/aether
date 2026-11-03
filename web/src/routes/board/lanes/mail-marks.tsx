import { memo, useEffect, useRef } from 'react'
import { ArrowDown, ArrowUp, MessageSquare } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { EdgeAnchor } from '@/routes/board/lanes/connectors'
import type { LaneEdge } from '@/routes/board/lanes/edges'
import type { LaneGeometry } from '@/routes/board/lanes/lane-row'
import type { Conversation, MailMark } from '@/routes/board/lanes/mail'

/** Marks, links and pinned buttons carry this, so moving onto one keeps the hovered lane that drew it. */
export const mailControl = { 'data-mail-control': '' }

export const MailMarks = memo(function MailMarks({ marks, anchors, g, height, focus, linked, title, onPick }: {
  marks: MailMark[]
  anchors: Map<string, EdgeAnchor>
  g: LaneGeometry
  height: number
  focus?: string
  /** Bundles drawn as links right now; their marks give way to them. */
  linked: (edge: LaneEdge) => boolean
  title: (edge: LaneEdge) => string
  onPick: (edges: LaneEdge[], x: number, y: number) => void
}) {
  // Only a mark that arrives while the view is open fades in, and only the first time it shows.
  const seen = useRef<Set<string> | null>(null)
  seen.current ??= new Set(marks.map((mark) => mark.key))
  const drawn: string[] = []
  useEffect(() => {
    for (const key of drawn) seen.current!.add(key)
  })
  return (
    <svg aria-hidden data-lane-mail className="pointer-events-none absolute top-0 overflow-visible" style={{ left: g.labelWidth, width: g.width, height }}>
      {marks.map((mark) => {
        const a = anchors.get(mark.lane)
        const edges = mark.edges.filter((edge) => !linked(edge))
        if (!a || edges.length === 0) return null
        drawn.push(mark.key)
        const count = edges.reduce((n, edge) => n + edge.messages!.ids.length, 0)
        const sent = edges.every((edge) => edge.from.lane === mark.lane)
        const own = !focus || edges.some((edge) => edge.from.lane === focus || edge.to.lane === focus)
        // Inside the bar's rounded ends and clear of the now line.
        const x = Math.min(g.width - 6, Math.max(6, mark.x))
        const dir = mark.side === 'top' ? -1 : 1
        const y = a.top + a.height / 2 + dir * (a.half + 2)
        const flip = x > g.width - 20
        return (
          <g
            key={mark.key}
            data-mail={sent ? 'sent' : 'received'}
            className={cn(own ? 'text-series-5' : 'text-icon-faint', !seen.current!.has(mark.key) && 'motion-safe:animate-fade-in')}
          >
            <title>{edges.map(title).join('\n')}</title>
            {sent
              ? <circle cx={x} cy={y} r={2.75} fill="currentColor" stroke="var(--canvas)" strokeWidth={1} />
              : <circle cx={x} cy={y} r={3} fill="var(--canvas)" stroke="currentColor" strokeWidth={1.5} />}
            {/* Under 768px facing marks on neighbouring lanes are too close for counts; the tooltip and popover keep them. */}
            {count > 1 && !g.compact && (
              <text
                x={flip ? x - 5 : x + 5}
                y={y + 3.5}
                textAnchor={flip ? 'end' : 'start'}
                className="fill-current text-ui-xs font-medium tabular-nums"
                style={{ paintOrder: 'stroke', stroke: 'var(--canvas)', strokeWidth: 3 }}
              >
                {count}
              </text>
            )}
            <rect
              {...mailControl}
              x={x - 8}
              y={y - 8}
              width={16}
              height={16}
              fill="transparent"
              pointerEvents="all"
              className="cursor-pointer"
              onClick={(event) => {
                event.stopPropagation()
                onPick(edges, x, y)
              }}
            />
          </g>
        )
      })}
    </svg>
  )
})

export function MailChip({ text }: { text: string }) {
  return (
    <span aria-hidden className="flex h-4 shrink-0 items-center gap-1 rounded-chip border border-series-5 bg-raised px-1 text-ui-xs font-medium text-text tabular-nums">
      <MessageSquare className="size-3 text-series-5" />
      {text}
    </span>
  )
}

export function OffscreenMail({ above, below, outside, g, top, bottom, nameOf, onJump }: {
  above: Conversation[]
  below: Conversation[]
  outside: Conversation[]
  g: LaneGeometry
  /** The visible content's top and bottom, in content coordinates. */
  top: number
  bottom: number
  nameOf: (c: Conversation) => string
  onJump: (lane: string) => void
}) {
  const total = (list: Conversation[]) => list.reduce((n, c) => n + c.sent + c.received, 0)
  const messages = (n: number) => `${n} ${n === 1 ? 'message' : 'messages'}`
  const right = g.labelWidth + g.width - 8
  const hint = (list: Conversation[], y: number, Icon: typeof ArrowUp) => (
    <Button
      {...mailControl}
      variant="secondary"
      size="sm"
      tabIndex={-1}
      className="pointer-events-auto absolute z-10 h-6 max-w-[min(360px,80%)] -translate-x-full gap-1 border-series-5 px-2 text-ui-xs"
      style={{ left: right, top: y }}
      onClick={() => onJump(list[0].lane)}
    >
      <Icon aria-hidden className="size-3 shrink-0 text-series-5" />
      <span className="min-w-0 truncate">{nameOf(list[0])}{list.length > 1 && ` +${list.length - 1}`}</span>
      <span className="shrink-0 text-muted tabular-nums">· {messages(total(list))}</span>
    </Button>
  )
  const away = total(outside)
  return (
    <>
      {above.length > 0 && hint(above, top + 4, ArrowUp)}
      {below.length > 0 && hint(below, bottom - 28, ArrowDown)}
      {away > 0 && (
        <span
          className="pointer-events-none absolute z-10 flex h-6 -translate-x-full items-center rounded-control border border-dashed border-seam bg-raised px-2 text-ui-xs whitespace-nowrap text-muted"
          style={{ left: right, top: top + (above.length > 0 ? 32 : 4) }}
        >
          {messages(away)} with {outside.length === 1 ? 'a session' : `${outside.length} sessions`} outside this view
        </span>
      )}
    </>
  )
}
