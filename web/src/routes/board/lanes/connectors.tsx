import { memo } from 'react'
import { TriangleAlert } from '@/components/icons'
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

export const edgeTone: Record<LaneEdge['kind'], string> = {
  message: 'text-series-5',
  overlap: 'text-state-needs-you',
}

function arrow(x: number, y: number, ux: number, uy: number): string {
  const bx = x - ux * 6
  const by = y - uy * 6
  return `${x},${y} ${bx - uy * 3.5},${by + ux * 3.5} ${bx + uy * 3.5},${by - ux * 3.5}`
}

interface Shape {
  d: string
  head?: { points: string; hollow: boolean }
  tail?: { x: number; y: number }
  mid: { x: number; y: number }
}

/** Model coordinates only: lane tops from the layout and x from the time window. */
export function edgeShape(edge: LaneEdge, a: EdgeAnchor, b: EdgeAnchor, g: LaneGeometry): Shape {
  const clamp = (x: number) => Math.min(g.width, Math.max(0, x))
  const x1 = clamp(xOf(edge.from.t, g.window, g.width))
  const y1 = a.top + a.height / 2
  const y2 = b.top + b.height / 2
  const dir = Math.sign(y2 - y1) || 1
  switch (edge.kind) {
    case 'overlap':
      return { d: `M${x1 - 4} ${y1}H${x1}V${y2}H${x1 - 4}`, mid: { x: x1, y: (y1 + y2) / 2 } }
    default: {
      const from = y1 + dir * a.half
      const stop = y2 - dir * b.half
      return {
        d: `M${x1} ${from}V${stop}`,
        head: { points: arrow(x1, stop, 0, dir), hollow: !edge.messages?.acked },
        tail: { x: x1, y: from },
        mid: { x: x1, y: (from + stop) / 2 },
      }
    }
  }
}

const dash: Partial<Record<LaneEdge['kind'], string>> = { message: '0.5 3', overlap: '4 2' }

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
    return [{ edge, shape: edgeShape(edge, a, b, g), tone: near ? edgeTone[edge.kind] : 'text-icon-faint', near }]
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
              strokeWidth={edge.kind === 'message' ? 1.5 : 1.25}
              strokeDasharray={dash[edge.kind]}
              strokeLinecap={edge.kind === 'message' ? 'round' : undefined}
              vectorEffect="non-scaling-stroke"
            />
            {shape.tail && <circle cx={shape.tail.x} cy={shape.tail.y} r={2} fill="currentColor" stroke="currentColor" strokeWidth={1.25} />}
            {shape.head && <polygon points={shape.head.points} fill={shape.head.hollow ? 'var(--canvas)' : 'currentColor'} stroke="currentColor" strokeWidth={1} strokeLinejoin="round" />}
          </g>
        ))}
      </svg>
      {shapes.map(({ edge, shape, tone }) => {
        const count = edge.messages && edge.messages.ids.length > 1 ? edge.messages.ids.length : undefined
        const kind = edge.messages?.kind && edge.messages.kind !== 'message' ? edge.messages.kind : undefined
        const files = edge.kind === 'overlap' ? edge.files?.length : undefined
        if (!count && !kind && !files) return null
        return (
          <span
            key={`${edge.key}:mark`}
            aria-hidden
            className={cn('pointer-events-none absolute flex h-4 -translate-x-1/2 -translate-y-1/2 items-center gap-0.5 rounded-chip border border-seam bg-raised px-1 text-ui-xs tabular-nums', tone)}
            style={{ left: g.labelWidth + shape.mid.x, top: shape.mid.y }}
          >
            {kind && <MessageKindGlyph kind={kind} />}
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
