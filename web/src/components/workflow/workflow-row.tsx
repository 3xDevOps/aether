import { memo } from 'react'
import type * as React from 'react'
import { ChevronRight } from '@/components/icons'
import { StatusDot } from '@/components/ui/status-dot'
import { useClock } from '@/lib/clock'
import type { Run } from '@/lib/types'
import { cn, focusRing, focusRingInset } from '@/lib/utils'
import { stateLabel as prStateLabel, usePullRequest } from '@/routes/diff/publish-pr'
import { elapsed } from '@/routes/missions/tree'
import type { WorkflowRow } from '@/components/workflow/flatten'
import { useStore } from '@/store'
import { countsText, workflowLabel, workflowTone, type WorkflowNode } from '@/store/workflow'

const step = 16

function Guides({ row }: { row: WorkflowRow }) {
  const own = (row.level - 2) * step + 8
  return (
    <span aria-hidden className="pointer-events-none absolute inset-y-0 left-0">
      {row.guides.slice(1).map((more, j) =>
        more ? <span key={j} className="absolute inset-y-0 border-l border-icon-faint" style={{ left: j * step + 8 }} /> : null,
      )}
      {row.level > 1 && <span className="absolute top-0 h-(--wire) w-2 border-b border-l border-icon-faint" style={{ left: own }} />}
      {row.level > 1 && !row.last && <span className="absolute top-(--wire) bottom-0 border-l border-icon-faint" style={{ left: own }} />}
      {row.expanded && <span className="absolute bottom-0 border-l border-icon-faint" style={{ left: own + step, top: 'calc(var(--wire) + 7px)' }} />}
    </span>
  )
}

function LiveElapsed({ from }: { from: string }) {
  useClock()
  return <>{elapsed(from, Date.now())}</>
}

const live = new Set(['working', 'waiting-agents', 'waiting-ci', 'waiting-tools', 'running'])

function Trailing({ node }: { node: WorkflowNode }) {
  if (node.counts) return <>{countsText(node.counts)}</>
  if (!node.startedAt) return null
  if (node.endedAt) return <>{elapsed(node.startedAt, node.endedAt)}</>
  return live.has(node.state) ? <LiveElapsed from={node.startedAt} /> : null
}

function TrackedPR({ run }: { run: Run }) {
  const pr = usePullRequest(run, false).feedback?.pull_request
  if (!pr) return null
  return (
    <a href={pr.url} target="_blank" rel="noreferrer" className={cn(focusRing, 'shrink-0 rounded-control tabular-nums hover:text-text hover:underline')}>
      #{pr.number} {prStateLabel(pr)}
    </a>
  )
}

/** The branch, and the pull request this tab last read for it; nothing here is fetched per row. */
function GitLine({ branch, runId }: { branch: string; runId?: string }) {
  const run = useStore((s) => (runId ? s.runs[runId] : undefined))
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-ui-xs text-muted">
      <span className="min-w-0 truncate rounded-chip bg-chrome px-1 font-code" title={branch}>{branch}</span>
      {run && <TrackedPR run={run} />}
    </span>
  )
}

interface RowProps {
  row: WorkflowRow
  active: boolean
  fresh: boolean
  onActivate: (row: WorkflowRow) => void
  onToggle: (row: WorkflowRow) => void
  register: (key: string, element: HTMLElement | null) => void
  extra?: (node: WorkflowNode, expanded: boolean | undefined) => React.ReactNode
}

/** Flattening builds fresh row objects on every tree change; a row whose node and place are unchanged skips its render. */
function sameRow(a: RowProps, b: RowProps): boolean {
  if (a.active !== b.active || a.fresh !== b.fresh || a.extra !== b.extra || a.onActivate !== b.onActivate || a.onToggle !== b.onToggle || a.register !== b.register) return false
  const x = a.row
  const y = b.row
  return x === y || (
    x.key === y.key && x.node === y.node && x.level === y.level && x.posinset === y.posinset && x.setsize === y.setsize &&
    x.expanded === y.expanded && x.surfaced === y.surfaced && x.nested === y.nested && x.last === y.last &&
    x.parentKey === y.parentKey && x.guides.join() === y.guides.join()
  )
}

export const WorkflowRowItem = memo(function WorkflowRowItem({ row, active, fresh, onActivate, onToggle, register, extra }: RowProps) {
  const { node, level, expanded, nested } = row
  const label = nested !== undefined ? `Nested workflow · ${nested} ${nested === 1 ? 'agent' : 'agents'}` : node.label
  const state = workflowLabel[node.state]
  const more = nested === undefined ? extra?.(node, expanded) : undefined
  return (
    <div
      ref={(element) => register(row.key, element)}
      role="treeitem"
      aria-level={level}
      aria-posinset={row.posinset}
      aria-setsize={row.setsize}
      aria-expanded={expanded}
      aria-label={nested !== undefined ? label : `${label}, ${state}${node.detail ? `, ${node.detail}` : ''}`}
      tabIndex={active ? 0 : -1}
      data-key={row.key}
      data-state={node.state}
      onClick={(event) => {
        if (event.target instanceof Element && event.target.closest('a,button,input,textarea,[data-extra]')) return
        onActivate(row)
      }}
      className={cn(
        focusRingInset,
        'relative min-w-0 rounded-control [--wire:14px] coarse:[--wire:22px]',
        fresh && 'animate-entrance motion-reduce:animate-none',
      )}
    >
      <Guides row={row} />
      <div className="flex h-7 min-w-0 cursor-default items-center gap-1.5 rounded-control pr-2 hover:bg-hover coarse:h-11" style={{ paddingLeft: (level - 1) * step }}>
        {expanded === undefined ? (
          <span aria-hidden className="w-4 shrink-0" />
        ) : (
          <span
            aria-hidden
            onClick={(event) => {
              event.stopPropagation()
              onToggle(row)
            }}
            className="flex size-4 shrink-0 items-center justify-center text-icon-faint hover:text-text"
          >
            <ChevronRight className={cn('size-3.5 transition-transform motion-reduce:transition-none', expanded && 'rotate-90')} />
          </span>
        )}
        {nested === undefined && <StatusDot tone={workflowTone[node.state]} pulse={node.state === 'working'} label={state} />}
        <span className="flex min-w-0 flex-1 items-baseline gap-1.5 text-ui-sm">
          <span className={cn('min-w-0 shrink truncate text-text', row.surfaced && 'font-medium')}>
            {label}
          </span>
          {nested === undefined && (
            <span className="min-w-0 flex-1 truncate text-muted">
              {node.detail ?? state}
            </span>
          )}
        </span>
        {nested === undefined && (
          <span className="shrink-0 text-ui-sm text-muted tabular-nums">
            <Trailing node={node} />
          </span>
        )}
      </div>
      {node.branch && nested === undefined && (
        <div className="pr-2 pb-1" style={{ paddingLeft: (level - 1) * step + 34 }}>
          <GitLine branch={node.branch} runId={node.runId} />
        </div>
      )}
      {more && <div data-extra style={{ paddingLeft: (level - 1) * step + 22 }}>{more}</div>}
    </div>
  )
}, sameRow)
