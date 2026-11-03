import { memo, useRef } from 'react'
import { ArrowDown, ArrowUp, MessageSquare } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { EdgeAnchor } from '@/routes/board/lanes/connectors'
import type { LaneEdge } from '@/routes/board/lanes/edges'
import type { LaneGeometry } from '@/routes/board/lanes/lane-row'
import type { Conversation, MailMark } from '@/routes/board/lanes/mail'

/**
 * Agent mail at rest: a dot where a bundle was sent and a ring where it arrived, just outside the bar edge
 * that faces the other lane. A mark whose link is drawn (focused lane, picked link) gives way to the link.
 */
export const MailMarks = memo(function MailMarks({ marks, anchors, g, height, focus, picked, title, onPick }: {
  marks: MailMark[]
  anchors: Map<string, EdgeAnchor>
  g: LaneGeometry
  height: number
  focus?: string
  picked?: string
  title: (mark: MailMark) => string
  onPick: (edge: LaneEdge, x: number, y: number) => void
}) {
  // Marks present on first render stay still; later ones fade in once.
  const first = useRef<Set<string> | null>(null)
  first.current ??= new Set(marks.map((mark) => mark.key))
  return (
    <svg aria-hidden data-lane-mail className="pointer-events-none absolute top-0 overflow-visible" style={{ left: g.labelWidth, width: g.width, height }}>
      {marks.map((mark) => {
        const a = anchors.get(mark.lane)
        if (!a) return null
        if (mark.edges.some((edge) => edge.key === picked || (focus && (edge.from.lane === focus || edge.to.lane === focus)))) return null
        // Inside the bar's rounded ends and clear of the now line.
        const x = Math.min(g.width - 6, Math.max(6, mark.x))
        const dir = mark.side === 'top' ? -1 : 1
        const y = a.top + a.height / 2 + dir * (a.half + 2)
        const flip = x > g.width - 20
        return (
          <g
            key={mark.key}
            data-mail={mark.sent ? 'sent' : 'received'}
            className={cn(focus ? 'text-icon-faint' : 'text-series-5', !first.current!.has(mark.key) && 'motion-safe:animate-fade-in')}
          >
            <title>{title(mark)}</title>
            {mark.sent
              ? <circle cx={x} cy={y} r={2.75} fill="currentColor" stroke="var(--canvas)" strokeWidth={1} />
              : <circle cx={x} cy={y} r={3} fill="var(--canvas)" stroke="currentColor" strokeWidth={1.5} />}
            {/* Under 768px facing marks on neighbouring lanes are too close for counts; the tooltip and popover keep them. */}
            {mark.count > 1 && !g.compact && (
              <text
                x={flip ? x - 5 : x + 5}
                y={y + 3.5}
                textAnchor={flip ? 'end' : 'start'}
                className="fill-current text-ui-xs font-medium tabular-nums"
                style={{ paintOrder: 'stroke', stroke: 'var(--canvas)', strokeWidth: 3 }}
              >
                {mark.count}
              </text>
            )}
            <rect
              x={x - 8}
              y={y - 8}
              width={16}
              height={16}
              fill="transparent"
              pointerEvents="all"
              className="cursor-pointer"
              onClick={(event) => {
                event.stopPropagation()
                onPick(mark.edges[0], x, y)
              }}
            />
          </g>
        )
      })}
    </svg>
  )
})

/** The focused lane's mail counts on a lane's label; strong on counterparts and the focused lane itself. */
export function MailChip({ text }: { text: string }) {
  return (
    <span aria-hidden className="flex h-4 shrink-0 items-center gap-1 rounded-chip border border-series-5 bg-raised px-1 text-ui-xs font-medium text-text tabular-nums">
      <MessageSquare className="size-3 text-series-5" />
      {text}
    </span>
  )
}

/** The focused lane's counterparts outside the visible rows, pinned to the edge they lie past, and mail with sessions outside the view. */
export function OffscreenMail({ above, below, outside, g, top, bottom, nameOf, onJump }: {
  above: Conversation[]
  below: Conversation[]
  outside: Conversation[]
  g: LaneGeometry
  /** The visible content's top and bottom, in content coordinates. */
  top: number
  bottom: number
  nameOf: (c: Pick<Conversation, 'lane' | 'run'>) => string
  onJump: (lane: string) => void
}) {
  const total = (list: Conversation[]) => list.reduce((n, c) => n + c.sent + c.received, 0)
  const messages = (n: number) => `${n} ${n === 1 ? 'message' : 'messages'}`
  const right = g.labelWidth + g.width - 8
  const hint = (list: Conversation[], y: number, Icon: typeof ArrowUp) => (
    <Button
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
          className="pointer-events-none absolute z-10 flex h-6 -translate-x-full items-center rounded-control whitespace-nowrap border border-dashed border-seam bg-raised px-2 text-ui-xs text-muted"
          style={{ left: right, top: top + (above.length > 0 ? 32 : 4) }}
        >
          {messages(away)} with {outside.length === 1 ? 'a session' : `${outside.length} sessions`} outside this view
        </span>
      )}
    </>
  )
}
