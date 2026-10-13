import { useMemo, useState, type MouseEvent } from 'react'
import { Button } from '@/components/ui/button'
import { ChevronDown, ChevronRight } from '@/components/icons'
import { StatusDot } from '@/components/ui/status-dot'
import { RunInputIndicator } from '@/components/run-input-indicator'
import { Skeleton } from '@/components/ui/skeleton'
import { RelativeTime } from '@/components/ui/relative-time'
import { useDelayed } from '@/lib/hooks'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import { useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { RunRow } from '@/store/selectors'
import { runKey, swarmParents } from '@/routes/board/map-layout'

export function RunList({ runs, empty }: { runs: RunRow[]; empty: string }) {
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const tree = useMemo(() => nestWorkers(runs), [runs])
  const shown = (id: string, workers: RunRow[]) =>
    expanded[id] ?? true ? workers : workers.filter((worker) => worker.state === 'needs-you')
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable && runs.length === 0)

  if (runs.length === 0) {
    if (unreachable) {
      // A dead token is not an unreachable server: nothing retries, only a fresh token helps.
      return (
        <div
          role="alert"
          className="border-y border-state-failed/35 bg-state-failed/10 px-3 py-2 text-ui leading-5 text-state-failed sm:px-4"
        >
          <span className="break-words whitespace-pre-wrap">
            {dead ? error : 'Cannot reach the server. Retrying.'}
          </span>
        </div>
      )
    }
    if (loading) {
      return (
        <div className="grid gap-0 border-y border-seam px-3 py-2 sm:px-4">
          <div className="h-7"><Skeleton className="size-full" /></div>
          <div className="h-7"><Skeleton className="size-full" /></div>
          <div className="h-7"><Skeleton className="size-full" /></div>
        </div>
      )
    }
    return hydrated ? (
      <div className="w-full px-3 py-4 sm:px-4 sm:py-5">
        <div className="border-y border-seam px-3 py-3">
          <p className="text-ui font-medium">{empty}</p>
          <p className="mt-1 text-ui-sm leading-4 text-muted">
            New runs will appear here as soon as they are launched.
          </p>
        </div>
      </div>
    ) : null
  }

  return (
    <div className="w-full px-3 py-2 sm:px-4 sm:py-3">
      <div className="border-y border-seam">
        <div className="hidden grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] gap-4 border-b bg-chrome/30 px-3 py-1.5 text-ui-sm font-medium text-muted md:grid">
          <span>Run</span>
          <span>Owner</span>
          <span>Status</span>
          <span className="text-right">Changed</span>
        </div>
        <ul>
          {tree.map(({ row, workers }) => (
            <li key={row.run.id} className="border-b last:border-b-0">
              <Row
                row={row}
                toggle={
                  workers.length > 0
                    ? {
                        count: workers.length,
                        expanded: expanded[row.run.id] ?? true,
                        onToggle: () =>
                          setExpanded((prev) => ({ ...prev, [row.run.id]: !(prev[row.run.id] ?? true) })),
                      }
                    : undefined
                }
              />
              {workers.length > 0 && (
                <ul
                  id={workersID(row.run.id)}
                  hidden={shown(row.run.id, workers).length === 0}
                  aria-label={`Workers of ${runLabel(row.run)}`}
                  className="ml-5 border-l border-seam"
                >
                  {shown(row.run.id, workers).map((worker) => (
                    <li key={worker.run.id} className="border-t border-seam first:border-t-0">
                      <Row row={worker} />
                    </li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

function nestWorkers(runs: RunRow[]): { row: RunRow; workers: RunRow[] }[] {
  const parents = swarmParents(runs)
  const nodes = new Map(runs.map((row) => [runKey(row), { row, workers: [] as RunRow[] }]))
  const top = new Set<{ row: RunRow; workers: RunRow[] }>()
  for (const row of runs) {
    const parent = nodes.get(parents.get(runKey(row)) ?? '')
    if (parent) parent.workers.push(row)
    top.add(parent ?? nodes.get(runKey(row))!)
  }
  return [...top]
}

const workersID = (runID: string) => `workers-${runID}`

function Row({
  row: { run, state, reason, owner },
  toggle,
}: {
  row: RunRow
  toggle?: { count: number; expanded: boolean; onToggle: () => void }
}) {
  const navigate = useStore((s) => s.navigate)
  return (
    <div
      onClick={(event: MouseEvent<HTMLDivElement>) => {
        const target = event.target
        if (
          target instanceof Element &&
          target.closest('button, a, input, select, textarea, [data-run-navigation-exempt]')
        ) {
          return
        }
        navigate('run', { runId: run.id })
      }}
      style={{ borderLeftColor: owner?.color }}
      className={cn(
        focusRing,
        'grid w-full cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 gap-y-1 border-l-2 border-l-transparent px-3 py-2 text-left transition-colors hover:bg-hover-chrome md:grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] md:items-center',
      )}
    >
      <div className="col-start-1 row-start-1 flex min-w-0 items-start gap-2 md:col-auto md:row-auto">
        {toggle && (
          <Button
            variant="ghost"
            size="icon-sm"
            label={`${toggle.expanded ? 'Hide' : 'Show'} ${toggle.count} ${toggle.count === 1 ? 'worker' : 'workers'}`}
            aria-expanded={toggle.expanded}
            aria-controls={workersID(run.id)}
            onClick={toggle.onToggle}
          >
            {toggle.expanded ? <ChevronDown /> : <ChevronRight />}
          </Button>
        )}
        <StatusDot tone={state} label={stateLabel[state]} className="mt-1.5" />
        <div className="min-w-0 flex-1">
          <button
            type="button"
            aria-label={runLabel(run)}
            onClick={(event) => {
              event.stopPropagation()
              navigate('run', { runId: run.id })
            }}
            className={cn(
              focusRing,
              'block min-h-[22px] coarse:min-h-11 max-w-full rounded-[2px] text-left',
            )}
          >
            <span className="block line-clamp-2 break-words text-ui font-medium leading-5">
              {runLabel(run)}
            </span>
          </button>
          <span className="mt-0.5 block break-words text-ui-sm leading-4 text-muted">
            <span>{run.harness}</span>
            <span aria-hidden> · </span>
            <span
              data-run-navigation-exempt={run.branch ? 'true' : undefined}
              className={run.branch ? 'select-text font-code' : undefined}
              title={run.branch || undefined}
              onMouseDown={
                run.branch
                  ? (event) => event.stopPropagation()
                  : undefined
              }
              onClick={
                run.branch
                  ? (event) => event.stopPropagation()
                  : undefined
              }
            >
              {run.branch || 'No branch assigned'}
            </span>
            {reason && (
              <>
                <span aria-hidden> · </span>
                <span>{reason}</span>
              </>
            )}
          </span>
          <span className="mt-0.5 block break-words text-ui-sm leading-4 text-muted md:hidden">
            {owner?.display_name ?? run.member_id}
          </span>
        </div>
      </div>
      <span className="hidden min-w-0 items-center gap-2 break-words text-ui-sm leading-4 text-muted md:flex">
        {owner?.display_name ?? run.member_id}
      </span>
      <span className="col-start-1 row-start-2 flex min-w-0 flex-wrap items-center gap-1.5 text-ui-sm text-muted md:col-auto md:row-auto">
        <span className="md:hidden">Status</span>
        <StatusChip state={state} />
        <RunInputIndicator run={run} />
      </span>
      <RelativeTime
        at={run.stateChangedAt}
        className="col-start-2 row-start-1 whitespace-nowrap text-right text-ui-sm tabular-nums text-muted md:col-auto md:row-auto"
        title={run.stateChangedAt}
      />
    </div>
  )
}

export function RunStatusChip({ run }: { run: RunRecord }) {
  return <StatusChip state={useRunPresentation(run).state} />
}

export function StatusChip({ state }: { state: PresentationState }) {
  return (
    <span className="inline-flex min-h-5 items-center gap-1.5 rounded-[2px] border border-seam/80 px-1.5 py-px text-ui-sm leading-4 text-text">
      <StatusDot tone={state} />
      <span>{stateLabel[state]}</span>
    </span>
  )
}
