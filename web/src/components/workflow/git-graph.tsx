import { memo } from 'react'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { cn } from '@/lib/utils'
import { rowHeight, type GitGraph, type GraphEdge, type GraphRow, type Lane } from '@/store/git-flow'

const laneGap = 14
const laneX = (lane: Lane) => 8 + lane * laneGap
const rowY = (row: number) => row * rowHeight + rowHeight / 2

function edgePath(edge: GraphEdge): string {
  const x1 = laneX(edge.fromLane)
  const x2 = laneX(edge.toLane)
  const y1 = rowY(edge.from)
  const y2 = rowY(edge.to)
  if (x1 === x2) return `M${x1} ${y1}V${y2}`
  const half = rowHeight / 2
  if (edge.bend === 'early') return `M${x1} ${y1}C${x1} ${y1 + half} ${x2} ${y1 + half} ${x2} ${y1 + rowHeight}V${y2}`
  return `M${x1} ${y1}V${y2 - rowHeight}C${x1} ${y2 - half} ${x2} ${y2 - half} ${x2} ${y2}`
}

const dash: Record<GraphEdge['style'], string | undefined> = { solid: undefined, dashed: '3 3', dotted: '1 3' }

function Node({ row, index }: { row: GraphRow; index: number }) {
  const y = rowY(index)
  switch (row.kind) {
    case 'head':
      return <circle cx={laneX(0)} cy={y} r={4.5} fill="var(--canvas)" stroke="currentColor" strokeWidth={1.5} className="text-accent" />
    case 'commit':
      return (
        <circle
          cx={laneX(row.lane)}
          cy={y}
          r={3.5}
          fill="currentColor"
          className={row.conflicts ? 'text-state-needs-you' : row.own ? 'text-accent' : 'text-icon-faint'}
        />
      )
    case 'base':
      return <circle cx={laneX(1)} cy={y} r={3} fill="var(--canvas)" stroke="currentColor" strokeWidth={1.25} className="text-icon-faint" />
    default:
      return null
  }
}

function Lanes({ graph }: { graph: GitGraph }) {
  const width = laneX(graph.lanes === 2 ? 1 : 0) + 8
  return (
    <svg aria-hidden width={width} height={graph.rows.length * rowHeight} className="pointer-events-none absolute top-0 left-0 overflow-visible">
      {graph.edges.map((edge) => (
        <path
          key={edge.key}
          d={edgePath(edge)}
          fill="none"
          stroke="currentColor"
          strokeWidth={1.5}
          strokeDasharray={dash[edge.style]}
          className={edge.own ? 'text-accent' : 'text-icon-faint'}
        />
      ))}
      {graph.rows.map((row, index) => <Node key={row.key} row={row} index={index} />)}
    </svg>
  )
}

function laneWidth(lanes: 1 | 2): number {
  return laneX(lanes === 2 ? 1 : 0) + 14
}

const CommitRow = memo(function CommitRow({ row }: { row: Extract<GraphRow, { kind: 'commit' }> }) {
  const { commit } = row
  return (
    <>
      <span className={cn('min-w-0 truncate', row.conflicts ? 'text-state-needs-you' : row.own ? 'text-text' : 'text-muted')} title={commit.subject}>
        {commit.subject}
      </span>
      {row.tip && <span className="shrink-0 truncate rounded-control bg-chrome px-1 font-code text-ui-xs text-muted max-w-32">{row.tip}</span>}
      {row.conflicts > 0 && (
        <span className="shrink-0 rounded-control bg-state-needs-you-soft px-1 text-ui-xs text-state-needs-you tabular-nums">
          {row.conflicts} {row.conflicts === 1 ? 'conflict' : 'conflicts'}
        </span>
      )}
      <span className="ml-auto flex shrink-0 items-baseline gap-1.5 pl-2 text-ui-xs text-muted tabular-nums">
        <span className="font-code" title={commit.sha}>{commit.short}</span>
        <span className="hidden max-w-32 truncate @min-[420px]/git:inline">
          {commit.author} · <RelativeTime at={commit.authored_at} compact />
        </span>
      </span>
    </>
  )
})

/** One 28px row per commit, under one hand-drawn SVG of lanes. `head` is the pending action's row. */
export function GitGraphView({
  graph,
  head,
  onExpand,
}: {
  graph: GitGraph
  head: React.ReactNode
  onExpand?: () => void
}) {
  return (
    <div className="relative min-w-0">
      <Lanes graph={graph} />
      <ol aria-label="Commits" className="min-w-0" style={{ paddingLeft: laneWidth(graph.lanes) }}>
        {graph.rows.map((row) => (
          <li key={row.key} className="flex min-w-0 items-center gap-2 text-ui-sm" style={{ height: rowHeight }}>
            {row.kind === 'head' && head}
            {row.kind === 'commit' && <CommitRow row={row} />}
            {row.kind === 'fold' && (
              <Button variant="link" size="sm" aria-expanded={false} onClick={onExpand} className="text-muted hover:text-text">
                {row.hidden} more {row.hidden === 1 ? 'commit' : 'commits'}
              </Button>
            )}
            {row.kind === 'truncated' && <span className="text-muted">Older commits not shown: the log was truncated</span>}
            {row.kind === 'base' && (
              <>
                <span className="min-w-0 truncate text-muted">
                  Merge base with <span className="font-code">{row.ref}</span>
                </span>
                <span className="ml-auto shrink-0 pl-2 font-code text-ui-xs text-muted" title={row.sha}>{row.sha.slice(0, 7)}</span>
              </>
            )}
          </li>
        ))}
      </ol>
    </div>
  )
}
