import { memo } from 'react'
import { Check, TriangleAlert, X } from '@/components/icons'
import { MessageKindGlyph } from '@/components/messages/message-row'
import { cn } from '@/lib/utils'
import { xOf } from '@/routes/board/lanes/axis'
import type { LaneEdge } from '@/routes/board/lanes/edges'
import type { LaneGeometry } from '@/routes/board/lanes/lane-row'

export interface EdgeAnchor {
  /** The lane's top and the half height of what an edge attaches to. */
  top: number
  height: number
  half: number
}

const handle = 16

export const edgeTone: Record<LaneEdge['kind'], string> = {
  orchestration: 'text-series-7',
  message: 'text-series-5',
  submission: 'text-muted',
  dependency: 'text-muted',
  overlap: 'text-state-needs-you',
}

function toneOf(edge: LaneEdge): string {
  if (edge.kind !== 'submission') return edgeTone[edge.kind]
  if (edge.submission?.state === 'accepted') return 'text-state-done'
  if (edge.submission?.state === 'rejected') return 'text-state-failed'
  return 'text-muted'
}

function arrow(x: number, y: number, ux: number, uy: number): string {
  const bx = x - ux * 6
  const by = y - uy * 6
  return `${x},${y} ${bx - uy * 3.5},${by + ux * 3.5} ${bx + uy * 3.5},${by - ux * 3.5}`
}

interface Shape {
  d: string
  head?: { points: string; hollow: boolean }
  tail?: { x: number; y: number; open: boolean }
  mid: { x: number; y: number }
}

/** Model coordinates only: lane tops from the layout and x from the time window. */
export function edgeShape(edge: LaneEdge, a: EdgeAnchor, b: EdgeAnchor, g: LaneGeometry): Shape {
  const clamp = (x: number) => Math.min(g.width, Math.max(0, x))
  const x1 = clamp(xOf(edge.from.t, g.window, g.width))
  const x2 = clamp(xOf(edge.to.t, g.window, g.width))
  const y1 = a.top + a.height / 2
  const y2 = b.top + b.height / 2
  const dir = Math.sign(y2 - y1) || 1
  switch (edge.kind) {
    case 'dependency':
      return { d: `M${x1} ${y1}C${x1 + handle} ${y1} ${x2 - handle} ${y2} ${x2} ${y2}`, mid: { x: (x1 + x2) / 2, y: (y1 + y2) / 2 } }
    case 'overlap':
      return { d: `M${x1 - 4} ${y1}H${x1}V${y2}H${x1 - 4}`, mid: { x: x1, y: (y1 + y2) / 2 } }
    case 'orchestration': {
      const start = y1 + dir * a.half
      if (x2 - x1 > 8) {
        return { d: `M${x1} ${start}C${x1} ${y2} ${x1} ${y2} ${x2} ${y2}`, head: { points: arrow(x2, y2, 1, 0), hollow: false }, mid: { x: x1, y: (start + y2) / 2 } }
      }
      const stop = y2 - dir * b.half
      return { d: `M${x1} ${start}L${x2} ${stop}`, head: { points: arrow(x2, stop, 0, dir), hollow: false }, mid: { x: x1, y: (start + stop) / 2 } }
    }
    default: {
      const from = y1 + dir * a.half
      const stop = y2 - dir * b.half
      return {
        d: `M${x1} ${from}V${stop}`,
        head: { points: arrow(x1, stop, 0, dir), hollow: edge.kind === 'message' && !edge.messages?.acked },
        tail: { x: x1, y: from, open: edge.kind === 'submission' },
        mid: { x: x1, y: (from + stop) / 2 },
      }
    }
  }
}

const dash: Partial<Record<LaneEdge['kind'], string>> = { dependency: '2 2', message: '0.5 3', overlap: '4 2' }

export interface DrawnEdge {
  edge: LaneEdge
  shape: Shape
  tone: string
  near: boolean
}

/** `focus` is a hovered or selected lane: its edges keep their colour and every other edge recedes, drawn first so the focused ones sit on top. */
export function drawEdges(edges: LaneEdge[], anchors: Map<string, EdgeAnchor>, g: LaneGeometry, focus?: string): DrawnEdge[] {
  const out = edges.flatMap((edge) => {
    const a = anchors.get(edge.from.lane)
    const b = anchors.get(edge.to.lane)
    if (!a || !b) return []
    const near = !focus || edge.from.lane === focus || edge.to.lane === focus
    return [{ edge, shape: edgeShape(edge, a, b, g), tone: near ? toneOf(edge) : 'text-icon-faint', near }]
  })
  return out.sort((x, y) => Number(x.near) - Number(y.near))
}

/** Strokes, ends and marks; drawn under the lanes so bars and labels stay readable. */
export const EdgeStrokes = memo(function EdgeStrokes({ shapes, g, height }: { shapes: DrawnEdge[]; g: LaneGeometry; height: number }) {
  return (
    <>
      <svg aria-hidden data-lane-edges className="pointer-events-none absolute top-0 overflow-visible" style={{ left: g.labelWidth, width: g.width, height }}>
        {shapes.map(({ edge, shape, tone }) => (
          <g key={edge.key} data-edge={edge.kind} className={tone}>
            <path
              d={shape.d}
              fill="none"
              stroke="currentColor"
              strokeWidth={edge.kind === 'orchestration' || edge.kind === 'message' ? 1.5 : 1.25}
              strokeDasharray={edge.kind === 'orchestration' && edge.crossOwner ? '5 3' : dash[edge.kind]}
              strokeLinecap={edge.kind === 'message' ? 'round' : undefined}
              vectorEffect="non-scaling-stroke"
            />
            {shape.tail && <circle cx={shape.tail.x} cy={shape.tail.y} r={shape.tail.open ? 2.5 : 2} fill={shape.tail.open ? 'var(--canvas)' : 'currentColor'} stroke="currentColor" strokeWidth={1.25} />}
            {shape.head && <polygon points={shape.head.points} fill={shape.head.hollow ? 'var(--canvas)' : 'currentColor'} stroke="currentColor" strokeWidth={1} strokeLinejoin="round" />}
          </g>
        ))}
      </svg>
      {shapes.map(({ edge, shape, tone }) => {
        const count = edge.messages && edge.messages.ids.length > 1 ? edge.messages.ids.length : undefined
        const kind = edge.messages?.kind && edge.messages.kind !== 'message' ? edge.messages.kind : undefined
        const decided = edge.submission?.state === 'accepted' ? Check : edge.submission?.state === 'rejected' ? X : undefined
        const files = edge.kind === 'overlap' ? edge.files?.length : undefined
        if (!count && !kind && !decided && !files) return null
        const Decided = decided
        return (
          <span
            key={`${edge.key}:mark`}
            aria-hidden
            className={cn('pointer-events-none absolute flex h-4 -translate-x-1/2 -translate-y-1/2 items-center gap-0.5 rounded-chip border border-seam bg-raised px-1 text-ui-xs tabular-nums', tone)}
            style={{ left: g.labelWidth + shape.mid.x, top: shape.mid.y }}
          >
            {kind && <MessageKindGlyph kind={kind} />}
            {Decided && <Decided className="size-3" />}
            {files !== undefined && <TriangleAlert className="size-3" />}
            {count ?? files}
          </span>
        )
      })}
    </>
  )
})

/** Transparent hit paths above the lanes, so a link stays clickable where it crosses a bar. */
export const EdgeHits = memo(function EdgeHits({ shapes, g, height, onPick }: {
  shapes: DrawnEdge[]
  g: LaneGeometry
  height: number
  onPick: (edge: LaneEdge, x: number, y: number) => void
}) {
  return (
    <svg aria-hidden className="pointer-events-none absolute top-0 overflow-visible" style={{ left: g.labelWidth, width: g.width, height }}>
      {shapes.map(({ edge, shape }) => (
        <path
          key={edge.key}
          d={shape.d}
          fill="none"
          stroke="transparent"
          strokeWidth={10}
          pointerEvents="stroke"
          className="cursor-pointer"
          onClick={(event) => {
            event.stopPropagation()
            onPick(edge, shape.mid.x, shape.mid.y)
          }}
        />
      ))}
    </svg>
  )
})
